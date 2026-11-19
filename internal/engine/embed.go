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

func embeddingHash(text string, config embedConfig) string {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(config.URL + "\x00" + config.Model + "\x00" + text))
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

func (store *embedStore) load(path string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.path != "" {
		return
	}
	store.path = path
	store.byHash = make(map[string][]float32)
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

func (store *embedStore) get(hash string) ([]float32, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	vector, ok := store.byHash[hash]
	return vector, ok
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

// embedScores uses every available vector. Cold stores backfill in bounded
// batches. The persisted vectors allow subsequent recalls to continue backfill.
func (engine *Engine) embedScores(ctx context.Context, query string, records []memory.MemoryRecord, exhaustive bool) map[string]float64 {
	config := embedConfigFromEnv()
	if config.URL == "" || strings.TrimSpace(query) == "" {
		return nil
	}
	cache := &engine.embeddings
	profile := embeddingHash(engine.config.ProfileID, config)
	cache.load(filepath.Join(filepath.Dir(engine.indexPath), "embeddings-"+profile+".jsonl"))
	ctx, cancel := context.WithTimeout(ctx, 3500*time.Millisecond)
	defer cancel()
	queryVector, err := embedTexts(ctx, []string{strings.TrimSpace(query)})
	if err != nil || len(queryVector) == 0 || len(queryVector[0]) == 0 {
		return nil
	}

	maxBackfill := 32
	if exhaustive {
		maxBackfill = 256
	}
	missing := make([]string, 0, min(len(records), maxBackfill))
	missingText := make([]string, 0, cap(missing))
	requested := make(map[string]bool)
	for _, record := range records {
		text := embeddingText(record)
		hash := embeddingHash(text, config)
		if _, ok := cache.get(hash); ok || requested[hash] {
			continue
		}
		if len(missing) >= maxBackfill {
			break
		}
		requested[hash] = true
		missing = append(missing, hash)
		missingText = append(missingText, text)
	}
	for start := 0; start < len(missingText) && ctx.Err() == nil; start += 32 {
		end := min(start+32, len(missingText))
		vectors, err := embedTexts(ctx, missingText[start:end])
		if err != nil {
			break
		}
		for i, vector := range vectors {
			if len(vector) == len(queryVector[0]) {
				cache.persist(missing[start+i], vector)
			}
		}
	}
	scores := make(map[string]float64, len(records))
	for _, record := range records {
		hash := embeddingHash(embeddingText(record), config)
		vector, ok := cache.get(hash)
		if !ok || len(vector) != len(queryVector[0]) {
			continue
		}
		scores[record.BlockID] = cosine(queryVector[0], vector)
	}
	return scores
}

func embeddingText(record memory.MemoryRecord) string {
	text := strings.TrimSpace(record.Title + ". " + record.Content)
	if len(text) <= 1800 {
		return text
	}
	runes := []rune(text)
	return string(runes[:min(1800, len(runes))])
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
