package zvec

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMiniLMModelRequiresCompleteExplicitDirectory(t *testing.T) {
	if _, err := EnsureMiniLMModel(context.Background(), t.TempDir()); err == nil {
		t.Fatal("incomplete explicit model directory accepted")
	}
}

func TestPinnedMiniLMArchiveInstallsWithoutPython(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	archive := filepath.Join(home, ".cache", "chroma", "onnx_models", "all-MiniLM-L6-v2", "onnx.tar.gz")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("pinned model archive unavailable: %v", err)
	}
	if err := verifyMiniLMArchive(archive); err != nil {
		t.Fatal(err)
	}
	modelDir := filepath.Join(t.TempDir(), "onnx")
	if err := installMiniLMArchive(archive, modelDir); err != nil {
		t.Fatal(err)
	}
	if err := checkModelFiles(modelDir); err != nil {
		t.Fatal(err)
	}
}

func TestMiniLMArchiveRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	archive := filepath.Join(root, "malicious.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(file)
	writer := tar.NewWriter(compressed)
	if err := writer.WriteHeader(&tar.Header{Name: "onnx/../escape", Mode: 0o644, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := installMiniLMArchive(archive, filepath.Join(root, "onnx")); err == nil {
		t.Fatal("malicious model archive was accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
		t.Fatalf("archive escaped model staging directory: %v", err)
	}
}
