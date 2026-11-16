//go:build purego

package zvec

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/frontier-ai-next/Um-mem/memory"
	"github.com/gofrs/flock"
	native "github.com/zvec-ai/zvec-go"
)

const writeBatchSize = 256

type EmbeddingSource interface {
	Embed(context.Context, []string) ([][]float32, error)
}

// NativeCollection wraps one physical Zvec leaf collection. The process owns
// the SDK initialization; callers close individual collections.
type NativeCollection struct {
	collection *native.Collection
	mu         sync.Mutex
}

var (
	initializeMu sync.Mutex
	initialized  bool
)

func OpenNativeCollection(path string, create bool) (*NativeCollection, error) {
	initializeMu.Lock()
	if !initialized {
		if _, err := EnsureNativeLibrary(context.Background()); err != nil {
			initializeMu.Unlock()
			return nil, fmt.Errorf("prepare Zvec C API: %w", err)
		}
		if err := native.Initialize(nil); err != nil {
			initializeMu.Unlock()
			return nil, fmt.Errorf("initialize Zvec: %w", err)
		}
		initialized = true
	}
	initializeMu.Unlock()
	if path == "" {
		return nil, errors.New("Zvec collection path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	lock := flock.New(path + ".open.lock")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("lock Zvec collection open: %w", err)
	}
	if !locked {
		return nil, errors.New("Zvec collection open lock timed out")
	}
	defer lock.Unlock()
	if _, err := os.Stat(path); err == nil {
		collection, err := native.Open(path, nil)
		if err != nil {
			return nil, fmt.Errorf("open Zvec collection: %w", err)
		}
		return &NativeCollection{collection: collection}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if !create {
		return nil, os.ErrNotExist
	}
	schema, err := newNativeSchema()
	if err != nil {
		return nil, err
	}
	defer schema.Destroy()
	collection, err := native.CreateAndOpen(path, schema, nil)
	if err != nil {
		return nil, fmt.Errorf("create Zvec collection: %w", err)
	}
	return &NativeCollection{collection: collection}, nil
}

func newNativeSchema() (*native.CollectionSchema, error) {
	schema := native.NewCollectionSchema("um_mem_leaf")
	if schema == nil {
		return nil, errors.New("create Zvec schema failed")
	}
	add := func(name string, kind native.DataType, dimensions uint32, params *native.IndexParams) error {
		field := native.NewFieldSchema(name, kind, false, dimensions)
		if field == nil {
			return fmt.Errorf("create Zvec field %q failed", name)
		}
		defer field.Destroy()
		if params != nil {
			if err := field.SetIndexParams(params); err != nil {
				return fmt.Errorf("index Zvec field %q: %w", name, err)
			}
		}
		if err := schema.AddField(field); err != nil {
			return fmt.Errorf("add Zvec field %q: %w", name, err)
		}
		return nil
	}
	for _, field := range []struct {
		name string
		kind native.DataType
	}{
		{"record_id", native.DataTypeString},
		{"source_sha256", native.DataTypeString},
		{"commit_revision", native.DataTypeInt64},
		{"chunk_start", native.DataTypeInt64},
		{"chunk_end", native.DataTypeInt64},
	} {
		if err := add(field.name, field.kind, 0, nil); err != nil {
			schema.Destroy()
			return nil, err
		}
	}
	fts, err := native.NewFTSIndexParams("standard", []string{"lowercase"}, "")
	if err != nil {
		schema.Destroy()
		return nil, err
	}
	if err := add("content", native.DataTypeString, 0, fts); err != nil {
		fts.Destroy()
		schema.Destroy()
		return nil, err
	}
	fts.Destroy()
	hnsw, err := native.NewHNSWIndexParams(native.MetricTypeCosine, 16, 200)
	if err != nil {
		schema.Destroy()
		return nil, err
	}
	if err := add("embedding", native.DataTypeVectorFP32, embeddingDimensions, hnsw); err != nil {
		hnsw.Destroy()
		schema.Destroy()
		return nil, err
	}
	hnsw.Destroy()
	return schema, nil
}

func (c *NativeCollection) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.collection == nil {
		return nil
	}
	err := c.collection.Close()
	c.collection = nil
	return err
}

func (c *NativeCollection) SearchCandidates(vector []float32, queryText string, limit int, hybrid bool) (dense, lexical []NativeCandidate, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.collection == nil {
		return nil, nil, errors.New("Zvec collection is closed")
	}
	if len(vector) != embeddingDimensions || limit < 1 || limit > 10000 {
		return nil, nil, errors.New("Zvec search requires a 384-dimensional vector and bounded limit")
	}
	dense, err = c.queryCandidates("embedding", vector, "", limit)
	if err != nil || !hybrid {
		return dense, nil, err
	}
	lexical, err = c.queryCandidates("content", nil, queryText, limit)
	return dense, lexical, err
}

func (c *NativeCollection) queryCandidates(field string, vector []float32, match string, limit int) ([]NativeCandidate, error) {
	query := native.NewSearchQuery()
	if query == nil {
		return nil, errors.New("create Zvec search query failed")
	}
	defer query.Destroy()
	if err := query.SetFieldName(field); err != nil {
		return nil, err
	}
	if err := query.SetTopK(limit); err != nil {
		return nil, err
	}
	if vector != nil {
		if err := query.SetQueryVector(vector); err != nil {
			return nil, err
		}
	} else {
		fts := native.NewFTS()
		if fts == nil {
			return nil, errors.New("create Zvec FTS query failed")
		}
		defer fts.Destroy()
		if err := fts.SetMatchString(match); err != nil {
			return nil, err
		}
		if err := query.SetFTS(fts); err != nil {
			return nil, err
		}
	}
	rows, err := c.collection.Query(query)
	if err != nil {
		return nil, fmt.Errorf("query Zvec collection: %w", err)
	}
	defer native.FreeDocs(rows)
	result := make([]NativeCandidate, 0, len(rows))
	for _, row := range rows {
		recordID, err := row.GetStringField("record_id")
		if err != nil || recordID == "" {
			return nil, errors.New("Zvec search hit lacks record identity")
		}
		sourceHash, err := row.GetStringField("source_sha256")
		if err != nil || sourceHash == "" {
			return nil, errors.New("Zvec search hit lacks source hash")
		}
		revision, err := row.GetInt64Field("commit_revision")
		if err != nil || revision < 1 {
			return nil, errors.New("Zvec search hit lacks commit revision")
		}
		score := float64(row.GetScore())
		if math.IsNaN(score) || math.IsInf(score, 0) {
			return nil, errors.New("Zvec search score is not finite")
		}
		result = append(result, NativeCandidate{ChunkID: row.GetPK(), RecordID: recordID,
			SourceSHA256: sourceHash, CommitRevision: revision, Score: score})
	}
	return result, nil
}

// Project mirrors Python's immutable, batch-wise native projection protocol.
// The canonical SQLite write and its outbox reservation must precede this call.
func (c *NativeCollection) Project(ctx context.Context, record memory.MemoryRecord, commitRevision int64, embedder EmbeddingSource) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.collection == nil {
		return "", errors.New("Zvec collection is closed")
	}
	if commitRevision <= 0 || embedder == nil {
		return "", errors.New("Zvec projection requires a commit revision and embedder")
	}
	chunks, sourceHash := Chunks(record)
	for offset := 0; offset < len(chunks); offset += writeBatchSize {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		batch := chunks[offset:min(offset+writeBatchSize, len(chunks))]
		ids := make([]string, len(batch))
		for i, chunk := range batch {
			ids[i] = chunk.ID
		}
		fetched, err := c.collection.Fetch(ids, nil)
		if err != nil {
			return "", fmt.Errorf("fetch Zvec projection: %w", err)
		}
		existing := make(map[string]*native.Doc, len(fetched))
		for _, doc := range fetched {
			existing[doc.GetPK()] = doc
		}
		missing := make([]Chunk, 0, len(batch))
		for _, chunk := range batch {
			if doc := existing[chunk.ID]; doc == nil {
				missing = append(missing, chunk)
			} else if err := verifyNativeDoc(doc, record.BlockID, sourceHash, commitRevision, chunk); err != nil {
				native.FreeDocs(fetched)
				return "", err
			}
		}
		native.FreeDocs(fetched)
		if len(missing) > 0 {
			texts := make([]string, len(missing))
			for i, chunk := range missing {
				texts[i] = chunk.Content
			}
			vectors, err := embedder.Embed(ctx, texts)
			if err != nil {
				return "", err
			}
			if len(vectors) != len(missing) {
				return "", errors.New("Zvec embedding count differs from missing chunks")
			}
			docs := make([]*native.Doc, 0, len(missing))
			for i, chunk := range missing {
				if len(vectors[i]) != embeddingDimensions {
					native.FreeDocs(docs)
					return "", errors.New("Zvec embedding dimensions differ from schema")
				}
				doc, err := newNativeDoc(record.BlockID, sourceHash, commitRevision, chunk, vectors[i])
				if err != nil {
					native.FreeDocs(docs)
					return "", err
				}
				docs = append(docs, doc)
			}
			result, err := c.collection.Upsert(docs)
			native.FreeDocs(docs)
			if err != nil {
				return "", fmt.Errorf("Zvec native upsert failed: %w", err)
			}
			if result == nil || result.ErrorCount != 0 || result.SuccessCount != uint64(len(missing)) {
				return "", fmt.Errorf("Zvec native upsert returned incomplete result: %+v", result)
			}
			if err := c.collection.Flush(); err != nil {
				return "", fmt.Errorf("flush Zvec projection: %w", err)
			}
		}
		verified, err := c.collection.Fetch(ids, nil)
		if err != nil {
			return "", fmt.Errorf("verify Zvec projection: %w", err)
		}
		if len(verified) != len(batch) {
			native.FreeDocs(verified)
			return "", errors.New("Zvec native projection is incomplete")
		}
		byID := make(map[string]*native.Doc, len(verified))
		for _, doc := range verified {
			byID[doc.GetPK()] = doc
		}
		for _, chunk := range batch {
			if err := verifyNativeDoc(byID[chunk.ID], record.BlockID, sourceHash, commitRevision, chunk); err != nil {
				native.FreeDocs(verified)
				return "", err
			}
		}
		native.FreeDocs(verified)
	}
	return chunks[0].ID, nil
}

func newNativeDoc(recordID, sourceHash string, revision int64, chunk Chunk, vector []float32) (*native.Doc, error) {
	doc := native.NewDoc()
	if doc == nil {
		return nil, errors.New("create Zvec document failed")
	}
	doc.SetPK(chunk.ID)
	for _, field := range []struct{ name, value string }{
		{"record_id", recordID}, {"source_sha256", sourceHash}, {"content", chunk.Content},
	} {
		if err := doc.AddStringField(field.name, field.value); err != nil {
			doc.Destroy()
			return nil, err
		}
	}
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"commit_revision", revision}, {"chunk_start", int64(chunk.Start)}, {"chunk_end", int64(chunk.End)},
	} {
		if err := doc.AddInt64Field(field.name, field.value); err != nil {
			doc.Destroy()
			return nil, err
		}
	}
	if err := doc.AddVectorFP32Field("embedding", vector); err != nil {
		doc.Destroy()
		return nil, err
	}
	return doc, nil
}

func verifyNativeDoc(doc *native.Doc, recordID, sourceHash string, revision int64, chunk Chunk) error {
	if doc == nil {
		return errors.New("Zvec native projection verification failed")
	}
	for _, field := range []struct{ name, value string }{
		{"record_id", recordID}, {"source_sha256", sourceHash}, {"content", chunk.Content},
	} {
		got, err := doc.GetStringField(field.name)
		if err != nil || got != field.value {
			return fmt.Errorf("Zvec native projection conflicts with immutable %s", field.name)
		}
	}
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"commit_revision", revision}, {"chunk_start", int64(chunk.Start)}, {"chunk_end", int64(chunk.End)},
	} {
		got, err := doc.GetInt64Field(field.name)
		if err != nil || got != field.value {
			return fmt.Errorf("Zvec native projection conflicts with immutable %s", field.name)
		}
	}
	return nil
}
