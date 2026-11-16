package zvec

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestMiniLMRequiresModel(t *testing.T) {
	if _, err := OpenMiniLMEmbedder(context.Background(), ""); err == nil {
		t.Fatal("empty MiniLM model directory accepted")
	}
}

func TestMiniLMPythonVectorParity(t *testing.T) {
	modelDir := os.Getenv("UM_MEM_TEST_MINILM_MODEL_DIR")
	if modelDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("cannot locate local MiniLM model: %v", err)
		}
		modelDir = filepath.Join(home, ".cache", "chroma", "onnx_models", "all-MiniLM-L6-v2", "onnx")
	}
	if _, err := os.Stat(filepath.Join(modelDir, "model.onnx")); err != nil {
		t.Skipf("local MiniLM model unavailable: %v", err)
	}
	data, err := os.ReadFile("testdata/python_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected [][]float32
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	embedder, err := OpenMiniLMEmbedder(context.Background(), modelDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := embedder.Close(); err != nil {
			t.Error(err)
		}
	}()
	got, err := embedder.Embed(context.Background(), []string{
		"The blue comet protocol is stored in memory.", "Ångström navigation compass",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(expected) {
		t.Fatalf("got %d vectors, want %d", len(got), len(expected))
	}
	for i := range got {
		if len(got[i]) != embeddingDimensions || len(expected[i]) != embeddingDimensions {
			t.Fatalf("vector %d has wrong dimensions", i)
		}
		for j := range got[i] {
			if delta := math.Abs(float64(got[i][j] - expected[i][j])); delta > 2e-6 {
				t.Fatalf("vector %d dimension %d differs from Python by %.9g", i, j, delta)
			}
		}
	}
	if err := embedder.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := embedder.Embed(context.Background(), []string{"closed"}); err == nil {
		t.Fatal("closed MiniLM embedder accepted input")
	}
}
