package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// Embeddings are optional: when no endpoint is configured, retrieval stays
// lexical and the hybrid ranker simply weighs the remaining signals. When one is
// configured, vectors live in a sidecar file next to the store — never in record
// metadata, which is reserved for governance.
type embedConfig struct {
	URL   string
	Key   string
	Model string
}

type embedStore struct {
	mu     sync.Mutex
	byHash map[string][]float32
	path   string
}

var embedCache = &embedStore{byHash: map[string][]float32{}}

func embedConfigFromEnv() embedConfig {
	config := embedConfig{
		URL:   strings.TrimSpace(os.Getenv("UM_MEM_EMBED_URL")),
		Key:   strings.TrimSpace(os.Getenv("UM_MEM_EMBED_KEY")),
		Model: strings.TrimSpace(os.Getenv("UM_MEM_EMBED_MODEL")),
	}
	if config.URL == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			data, readErr := os.ReadFile(filepath.Join(home, ".um-mem", "embed.json"))
			if readErr == nil {
				var file embedConfig
				if json.Unmarshal(data, &file) == nil {
					if config.URL == "" {
						config.URL = strings.TrimSpace(file.URL)
					}
					if config.Key == "" {
						config.Key = strings.TrimSpace(file.Key)
					}
					if config.Model == "" {
						config.Model = strings.TrimSpace(file.Model)
					}
				}
			}
		}
	}
	if config.Model == "" {
		config.Model = "Qwen3-Embedding-4B"
	}
	return config
}

func embeddingHash(text string) string {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(text))
	hasher.Write([]byte(embedConfigFromEnv().Model))
	return strconvUint(hasher.Sum64())
}

func strconvUint(value uint64) string {
	const digits = "0123456789abcdef"
	if value == 0 {
		return "0"
	}
	buf := make([]byte, 0, 16)
	for value > 0 {
		buf = append([]byte{digits[value&0xf]}, buf...)
		value >>= 4
	}
	return string(buf)
}

// embedTexts asks the endpoint for vectors, batching to keep the number of round
// trips low when a store is filled for the first time.
func embedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	config := embedConfigFromEnv()
	if config.URL == "" || len(texts) == 0 {
		return nil, errors.New("embeddings are not configured")
	}
	endpoint := strings.TrimRight(config.URL, "/") + "/embeddings"
	body, err := json.Marshal(map[string]any{"model": config.Model, "input": texts,
		"encoding_format": "float"})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if config.Key != "" {
		request.Header.Set("Authorization", "Bearer "+config.Key)
	}
	client := &http.Client{Timeout: 90 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return nil, errors.New("embedding endpoint returned " + response.Status)
	}
	var payload struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, err
	}
	out := make([][]float32, len(texts))
	for _, item := range payload.Data {
		if item.Index >= 0 && item.Index < len(out) {
			out[item.Index] = item.Embedding
		}
	}
	return out, nil
}

func (store *embedStore) load(storePath string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.path != "" {
		return
	}
	store.path = filepath.Join(storePath, ".UM-MEM", "embeddings.jsonl")
	file, err := os.Open(store.path)
	if err != nil {
		return
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	for {
		var line struct {
			Hash string    `json:"hash"`
			Vec  []float32 `json:"vec"`
		}
		if err := decoder.Decode(&line); err != nil {
			break
		}
		if line.Hash != "" && len(line.Vec) > 0 {
			store.byHash[line.Hash] = line.Vec
		}
	}
}

func (store *embedStore) persist(hash string, vec []float32) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.byHash == nil {
		store.byHash = map[string][]float32{}
	}
	store.byHash[hash] = vec
	if store.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		return
	}
	file, err := os.OpenFile(store.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	payload, err := json.Marshal(map[string]any{"hash": hash, "vec": vec})
	if err != nil {
		return
	}
	_, _ = file.Write(append(payload, '\n'))
}

// embedScores returns cosine similarity for the query against every record that
// already has a vector. Missing records are filled on demand, capped so a recall
// never blocks on a large backfill.
func (engine *Engine) embedScores(ctx context.Context, query string, records []memory.MemoryRecord) map[string]float64 {
	config := embedConfigFromEnv()
	if config.URL == "" || strings.TrimSpace(query) == "" {
		return nil
	}
	embedCache.load(engine.config.RepoRoot)

	missing := make([]memory.MemoryRecord, 0, 64)
	missingText := make([]string, 0, 64)
	for _, record := range records {
		text := strings.TrimSpace(record.Title + ". " + record.Content)
		hash := embeddingHash(text)
		if _, ok := embedCache.byHash[hash]; ok {
			continue
		}
		if len(missing) >= 32 {
			break
		}
		missing = append(missing, record)
		missingText = append(missingText, text)
	}
	if len(missingText) > 0 {
		vectors, err := embedTexts(ctx, missingText)
		if err == nil {
			for i, vector := range vectors {
				if len(vector) > 0 {
					embedCache.persist(embeddingHash(missingText[i]), vector)
				}
			}
		}
	}

	queryVector, err := embedTexts(ctx, []string{strings.TrimSpace(query)})
	if err != nil || len(queryVector) == 0 || len(queryVector[0]) == 0 {
		return nil
	}
	scores := make(map[string]float64, len(records))
	for _, record := range records {
		hash := embeddingHash(strings.TrimSpace(record.Title + ". " + record.Content))
		vector, ok := embedCache.byHash[hash]
		if !ok || len(vector) != len(queryVector[0]) {
			continue
		}
		scores[record.BlockID] = cosine(queryVector[0], vector)
	}
	return scores
}

func cosine(left, right []float32) float64 {
	var dot, leftNorm, rightNorm float64
	for i := range left {
		l := float64(left[i])
		r := float64(right[i])
		dot += l * r
		leftNorm += l * l
		rightNorm += r * r
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}
