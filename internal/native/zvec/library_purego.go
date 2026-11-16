//go:build purego

package zvec

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

const (
	zvecRelease            = "v0.7.0"
	zvecReleaseURL         = "https://github.com/zvec-ai/zvec-go/releases/download/" + zvecRelease + "/"
	maxNativeArchive int64 = 64 << 20
	maxNativeLibrary int64 = 64 << 20
)

type nativeArchive struct {
	asset, archiveSHA, member, librarySHA string
	zip                                   bool
}

// Digests are pinned to the zvec-go v0.7.0 release assets. Only the C API
// library is extracted; headers and archive paths are never installed.
func selectedNativeArchive() (nativeArchive, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return nativeArchive{"zvec-libs-windows-x64.zip",
			"d24ab3e360e3ca3cf65bf42ae347ddce1e953b3f8ea58d34b8d4e6cf0dc0f2b3",
			"windows_amd64/zvec_c_api.dll",
			"214d2dabb902075dd8f8650489253a1a1930f3b4a001c01d89445dcff4e180a1", true}, nil
	case "linux/amd64":
		return nativeArchive{"zvec-libs-linux-x64.tar.gz",
			"7a466f501050b7debe04fd64da52b98634910bb8a8d22083412fe511134cd088",
			"linux_amd64/libzvec_c_api.so",
			"b41fe8aba208b74b083e37849572284e7c091180869cba876b34c89e5833e514", false}, nil
	case "linux/arm64":
		return nativeArchive{"zvec-libs-linux-arm64.tar.gz",
			"6dfa9e02be9a69309b20eb5660658c0b3cb3f41b3197ba2391fb43e804122971",
			"linux_arm64/libzvec_c_api.so",
			"a337539b2f6250d520933599dfc7c889662634e23d8c6aea69dd47941c9e0707", false}, nil
	case "darwin/arm64":
		return nativeArchive{"zvec-libs-darwin-arm64.tar.gz",
			"c4ed1cc5b795c4538618e0e3b80953c8683e7a011265eaacc2af8192a67eb762",
			"darwin_arm64/libzvec_c_api.dylib",
			"c1037884b925445289b60afa0bcfd3de790da25b79584ed6eb526dad470cc8eb", false}, nil
	default:
		return nativeArchive{}, fmt.Errorf("Zvec v0.7.0 has no pinned C API artifact for %s/%s; set ZVEC_LIBRARY_PATH to a compatible library", runtime.GOOS, runtime.GOARCH)
	}
}

// EnsureNativeLibrary respects an explicit SDK override or installs a
// checksum-verified, platform-specific C API library in the user cache.
// Download and extraction are serialized between processes.
func EnsureNativeLibrary(ctx context.Context) (string, error) {
	if explicit := os.Getenv("ZVEC_LIBRARY_PATH"); explicit != "" {
		return explicit, nil
	}
	spec, err := selectedNativeArchive()
	if err != nil {
		return "", err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(cache, "um-mem", "native", "zvec", zvecRelease,
		runtime.GOOS+"_"+runtime.GOARCH)
	library := filepath.Join(root, filepath.Base(spec.member))
	if err := verifyNativeFile(library, spec.librarySHA, maxNativeLibrary); err == nil {
		return setNativeLibraryPath(library)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("cached Zvec library is invalid: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	lock := flock.New(filepath.Join(root, ".install.lock"))
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 100*time.Millisecond)
	if err != nil {
		return "", err
	}
	if !locked {
		return "", errors.New("Zvec library installation lock was not acquired")
	}
	defer lock.Unlock()
	if err := verifyNativeFile(library, spec.librarySHA, maxNativeLibrary); err == nil {
		return setNativeLibraryPath(library)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("cached Zvec library is invalid: %w", err)
	}
	archive := filepath.Join(root, spec.asset)
	if _, err := os.Stat(archive); errors.Is(err, os.ErrNotExist) {
		if err := downloadNativeArchive(ctx, archive, spec); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if err := verifyNativeFile(archive, spec.archiveSHA, maxNativeArchive); err != nil {
		return "", fmt.Errorf("cached Zvec archive is invalid: %w", err)
	}
	if err := extractNativeLibrary(archive, library, spec); err != nil {
		return "", err
	}
	if err := verifyNativeFile(library, spec.librarySHA, maxNativeLibrary); err != nil {
		return "", err
	}
	return setNativeLibraryPath(library)
}

func setNativeLibraryPath(path string) (string, error) {
	if err := os.Setenv("ZVEC_LIBRARY_PATH", path); err != nil {
		return "", err
	}
	return path, nil
}

func verifyNativeFile(path, expected string, limit int64) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
		return errors.New("Zvec native file has an invalid size or type")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return errors.New("Zvec native file SHA256 differs from pinned source")
	}
	return nil
}

func downloadNativeArchive(ctx context.Context, destination string, spec nativeArchive) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, zvecReleaseURL+spec.asset, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download Zvec C API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download Zvec C API: HTTP %d", response.StatusCode)
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".zvec-archive-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if copied, err := io.Copy(file, io.LimitReader(response.Body, maxNativeArchive+1)); err != nil {
		file.Close()
		return err
	} else if copied > maxNativeArchive {
		file.Close()
		return errors.New("Zvec C API archive exceeds size limit")
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := verifyNativeFile(file.Name(), spec.archiveSHA, maxNativeArchive); err != nil {
		return err
	}
	return os.Rename(file.Name(), destination)
}

func extractNativeLibrary(archive, destination string, spec nativeArchive) error {
	stage, err := os.CreateTemp(filepath.Dir(destination), ".zvec-library-*")
	if err != nil {
		return err
	}
	defer os.Remove(stage.Name())
	var source io.ReadCloser
	var size int64
	if spec.zip {
		reader, err := zip.OpenReader(archive)
		if err != nil {
			stage.Close()
			return err
		}
		defer reader.Close()
		for _, member := range reader.File {
			if member.Name != spec.member {
				continue
			}
			if source != nil || member.UncompressedSize64 < 1 || member.UncompressedSize64 > uint64(maxNativeLibrary) ||
				!member.Mode().IsRegular() {
				stage.Close()
				return errors.New("Zvec C API archive has an invalid library member")
			}
			source, err = member.Open()
			if err != nil {
				stage.Close()
				return err
			}
			size = int64(member.UncompressedSize64)
		}
		if source == nil {
			stage.Close()
			return errors.New("Zvec C API archive has no library member")
		}
	} else {
		file, err := os.Open(archive)
		if err != nil {
			stage.Close()
			return err
		}
		defer file.Close()
		compressed, err := gzip.NewReader(file)
		if err != nil {
			stage.Close()
			return err
		}
		defer compressed.Close()
		reader := tar.NewReader(compressed)
		found := false
		for {
			header, err := reader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				stage.Close()
				return err
			}
			if strings.TrimPrefix(header.Name, "./") != spec.member {
				continue
			}
			if found || header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > maxNativeLibrary {
				stage.Close()
				return errors.New("Zvec C API archive has an invalid library member")
			}
			found = true
			size = header.Size
			if _, err := io.CopyN(stage, reader, size); err != nil {
				stage.Close()
				return err
			}
		}
		if !found {
			stage.Close()
			return errors.New("Zvec C API archive has no library member")
		}
	}
	if source != nil {
		_, err = io.CopyN(stage, source, size)
		closeErr := source.Close()
		if err != nil || closeErr != nil {
			stage.Close()
			return errors.Join(err, closeErr)
		}
	}
	if err := stage.Close(); err != nil {
		return err
	}
	if err := verifyNativeFile(stage.Name(), spec.librarySHA, maxNativeLibrary); err != nil {
		return err
	}
	return os.Rename(stage.Name(), destination)
}
