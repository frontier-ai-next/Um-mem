package zvec

import (
	"archive/tar"
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
	"strings"
	"time"

	"github.com/gofrs/flock"
)

const (
	miniLMArchiveURL = "https://chroma-onnx-models.s3.amazonaws.com/all-MiniLM-L6-v2/onnx.tar.gz"
	miniLMArchiveSHA = "913d7300ceae3b2dbc2c50d1de4baacab4be7b9380491c27fab7418616a16ec3"
	maxModelArchive  = 100 << 20
	maxModelExtract  = 200 << 20
)

var modelFiles = map[string]bool{
	"config.json": true, "model.onnx": true, "special_tokens_map.json": true,
	"tokenizer.json": true, "tokenizer_config.json": true, "vocab.txt": true,
}

// EnsureMiniLMModel reuses an explicit directory or a verified cached model.
// A clean Go installation downloads Chroma's pinned archive into UM-MEM's
// cache; no Python package or process is involved.
func EnsureMiniLMModel(ctx context.Context, explicitDir string) (string, error) {
	if explicitDir != "" {
		return explicitDir, checkModelFiles(explicitDir)
	}
	home, err := os.UserHomeDir()
	if err == nil {
		legacy := filepath.Join(home, ".cache", "chroma", "onnx_models", "all-MiniLM-L6-v2", "onnx")
		if checkModelFiles(legacy) == nil {
			return legacy, nil
		}
	}
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(cacheRoot, "um-mem", "models", "all-MiniLM-L6-v2")
	modelDir := filepath.Join(root, "onnx")
	if checkModelFiles(modelDir) == nil {
		return modelDir, nil
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
		return "", errors.New("MiniLM model installation lock was not acquired")
	}
	defer lock.Unlock()
	if checkModelFiles(modelDir) == nil {
		return modelDir, nil
	}
	if _, err := os.Stat(modelDir); err == nil {
		return "", errors.New("MiniLM model directory exists but is incomplete")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	archivePath := filepath.Join(root, "onnx.tar.gz")
	if _, err := os.Stat(archivePath); errors.Is(err, os.ErrNotExist) {
		if err := downloadMiniLMArchive(ctx, archivePath); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if err := verifyMiniLMArchive(archivePath); err != nil {
		return "", err
	}
	if err := installMiniLMArchive(archivePath, modelDir); err != nil {
		return "", err
	}
	return modelDir, checkModelFiles(modelDir)
}

func checkModelFiles(dir string) error {
	for name := range modelFiles {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("MiniLM model file %q is unavailable: %w", name, err)
		}
		if info.IsDir() {
			return fmt.Errorf("MiniLM model file %q is a directory", name)
		}
	}
	return nil
}

func downloadMiniLMArchive(ctx context.Context, destination string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, miniLMArchiveURL, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download MiniLM model: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download MiniLM model: HTTP %d", response.StatusCode)
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".onnx-archive-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if copied, err := io.Copy(file, io.LimitReader(response.Body, maxModelArchive+1)); err != nil {
		file.Close()
		return err
	} else if copied > maxModelArchive {
		file.Close()
		return errors.New("MiniLM model archive exceeds size limit")
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := verifyMiniLMArchive(file.Name()); err != nil {
		return err
	}
	return os.Rename(file.Name(), destination)
}

func verifyMiniLMArchive(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maxModelArchive {
		return errors.New("MiniLM model archive exceeds size limit")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != miniLMArchiveSHA {
		return errors.New("MiniLM model archive SHA256 differs from pinned source")
	}
	return nil
}

func installMiniLMArchive(archivePath, modelDir string) error {
	root := filepath.Dir(modelDir)
	stage, err := os.MkdirTemp(root, ".onnx-extract-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	var total int64
	seen := make(map[string]bool)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(header.Name, "./")
		if name == "onnx/" && header.Typeflag == tar.TypeDir {
			continue
		}
		if !strings.HasPrefix(name, "onnx/") || !modelFiles[strings.TrimPrefix(name, "onnx/")] ||
			header.Typeflag != tar.TypeReg || seen[name] || header.Size < 0 || header.Size > maxModelExtract-total {
			return fmt.Errorf("MiniLM archive contains an invalid member %q", name)
		}
		seen[name] = true
		total += header.Size
		target := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(output, reader, header.Size)
		closeErr := output.Close()
		if copyErr != nil || closeErr != nil {
			return errors.Join(copyErr, closeErr)
		}
	}
	if err := checkModelFiles(filepath.Join(stage, "onnx")); err != nil {
		return err
	}
	return os.Rename(filepath.Join(stage, "onnx"), modelDir)
}
