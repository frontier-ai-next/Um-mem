package zvec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/pipelines"
)

const embeddingDimensions = 384

// MiniLMEmbedder runs the same ONNX model and tokenizer used by the Python
// Chroma ONNXMiniLM_L6_V2 embedding function. Its model directory must contain
// model.onnx and tokenizer.json from that distribution.
type MiniLMEmbedder struct {
	mu       sync.Mutex
	session  *hugot.Session
	pipeline *pipelines.FeatureExtractionPipeline
}

func OpenMiniLMEmbedder(ctx context.Context, modelDir string) (*MiniLMEmbedder, error) {
	if modelDir == "" {
		return nil, errors.New("MiniLM model directory is required")
	}
	for _, name := range []string{"model.onnx", "tokenizer.json"} {
		info, err := os.Stat(filepath.Join(modelDir, name))
		if err != nil {
			return nil, fmt.Errorf("MiniLM model file %q is unavailable: %w", name, err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("MiniLM model file %q is a directory", name)
		}
	}
	session, err := hugot.NewGoSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("open Go ONNX session: %w", err)
	}
	pipeline, err := hugot.NewPipeline(session, hugot.FeatureExtractionConfig{
		ModelPath: modelDir, Name: "umMemMiniLM", OnnxFilename: "model.onnx",
		Options: []hugot.FeatureExtractionOption{pipelines.WithNormalization()},
	})
	if err != nil {
		_ = session.Destroy()
		return nil, fmt.Errorf("load MiniLM pipeline: %w", err)
	}
	return &MiniLMEmbedder{session: session, pipeline: pipeline}, nil
}

func (e *MiniLMEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pipeline == nil {
		return nil, errors.New("MiniLM embedder is closed")
	}
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	output, err := e.pipeline.RunPipeline(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("embed with MiniLM: %w", err)
	}
	if len(output.Embeddings) != len(texts) {
		return nil, fmt.Errorf("MiniLM returned %d vectors for %d texts", len(output.Embeddings), len(texts))
	}
	for i, vector := range output.Embeddings {
		if len(vector) != embeddingDimensions {
			return nil, fmt.Errorf("MiniLM vector %d has %d dimensions, expected %d", i, len(vector), embeddingDimensions)
		}
	}
	return output.Embeddings, nil
}

func (e *MiniLMEmbedder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session == nil {
		return nil
	}
	e.pipeline = nil
	session := e.session
	e.session = nil
	return session.Destroy()
}
