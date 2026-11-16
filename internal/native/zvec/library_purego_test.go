//go:build purego

package zvec

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeLibraryArchiveExtractionIsPinnedAndRestricted(t *testing.T) {
	content := []byte("test native library")
	digest := sha256.Sum256(content)
	for _, zipped := range []bool{false, true} {
		name := "tar"
		if zipped {
			name = "zip"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			archive := filepath.Join(root, "library.archive")
			file, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			member := "platform/libzvec_c_api.so"
			if zipped {
				writer := zip.NewWriter(file)
				for _, name := range []string{"../escape", member} {
					header := &zip.FileHeader{Name: name, Method: zip.Deflate}
					header.SetMode(0o644)
					entry, err := writer.CreateHeader(header)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := entry.Write(content); err != nil {
						t.Fatal(err)
					}
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				compressed := gzip.NewWriter(file)
				writer := tar.NewWriter(compressed)
				for _, name := range []string{"../escape", member} {
					if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o644,
						Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
						t.Fatal(err)
					}
					if _, err := writer.Write(content); err != nil {
						t.Fatal(err)
					}
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				if err := compressed.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			spec := nativeArchive{member: member, librarySHA: hex.EncodeToString(digest[:]), zip: zipped}
			library := filepath.Join(root, "libzvec_c_api.so")
			if err := extractNativeLibrary(archive, library, spec); err != nil {
				t.Fatal(err)
			}
			if err := verifyNativeFile(library, spec.librarySHA, maxNativeLibrary); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
				t.Fatalf("archive wrote an unrelated member: %v", err)
			}
			if err := verifyNativeFile(library, "wrong", maxNativeLibrary); err == nil {
				t.Fatal("changed native library digest was accepted")
			}
		})
	}
}

func TestNativeLibraryAutoinstall(t *testing.T) {
	if os.Getenv("UM_MEM_TEST_ZVEC_AUTOINSTALL") != "1" {
		t.Skip("set UM_MEM_TEST_ZVEC_AUTOINSTALL=1 for a release archive integration check")
	}
	if os.Getenv("ZVEC_LIBRARY_PATH") != "" {
		t.Fatal("autoinstall test requires ZVEC_LIBRARY_PATH to be unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	path, err := EnsureNativeLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := selectedNativeArchive()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyNativeFile(path, spec.librarySHA, maxNativeLibrary); err != nil {
		t.Fatal(err)
	}
	collection, err := OpenNativeCollection(filepath.Join(t.TempDir(), "collection"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := collection.Close(); err != nil {
		t.Fatal(err)
	}
}
