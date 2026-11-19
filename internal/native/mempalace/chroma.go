package mempalace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	chroma "github.com/amikos-tech/chroma-go/pkg/api/v2"
	"github.com/amikos-tech/chroma-go/pkg/embeddings"
	"github.com/frontier-ai-next/Um-mem/internal/paths"
	"github.com/frontier-ai-next/Um-mem/memory"
	_ "modernc.org/sqlite"
)

type NativeRow struct {
	ID        string
	Document  string
	Metadata  map[string]any
	Embedding []float32
}

type NativeHit struct {
	NativeRow
	Distance float64
}

type PurgeResult struct {
	OK                  bool   `json:"ok"`
	Error               string `json:"error,omitempty"`
	NativeExactIDAbsent bool   `json:"native_exact_id_absent,omitempty"`
	NativeEdgesAbsent   bool   `json:"native_edges_absent,omitempty"`
}

// NativeClient owns one embedded Chroma runtime over the same on-disk palace
// that Python MemPalace uses. Callers must close it before another process
// takes ownership of that palace.
type NativeClient struct {
	client      chroma.Client
	workspaceID string
	path        string
	mu          sync.Mutex
}

// The MemPalace projector supplies and queries vectors explicitly. Opening a
// collection must not initialize Chroma's separate default embedding model.
type suppliedVectors struct{}

func (suppliedVectors) EmbedDocuments(context.Context, []string) ([]embeddings.Embedding, error) {
	return nil, errors.New("MemPalace requires supplied document embeddings")
}
func (suppliedVectors) EmbedQuery(context.Context, string) (embeddings.Embedding, error) {
	return nil, errors.New("MemPalace requires a supplied query embedding")
}
func (suppliedVectors) Name() string                                  { return "um-mem-supplied-vectors" }
func (suppliedVectors) GetConfig() embeddings.EmbeddingFunctionConfig { return nil }
func (suppliedVectors) DefaultSpace() embeddings.DistanceMetric       { return embeddings.COSINE }
func (suppliedVectors) SupportedSpaces() []embeddings.DistanceMetric {
	return []embeddings.DistanceMetric{embeddings.COSINE}
}

func OpenNativeClient(repoRoot, workspaceID string) (*NativeClient, error) {
	if repoRoot == "" || workspaceID == "" {
		return nil, errors.New("MemPalace native client requires repository and workspace")
	}
	path, err := paths.PalacePathFor(repoRoot, workspaceID)
	if err != nil {
		return nil, err
	}
	return openNativePath(path, workspaceID)
}

func openNativePath(path, workspaceID string) (*NativeClient, error) {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return nil, err
	}
	client, err := chroma.NewPersistentClient(chroma.WithPersistentPath(path))
	if err != nil {
		return nil, fmt.Errorf("open embedded Chroma palace: %w", err)
	}
	return &NativeClient{client: client, workspaceID: workspaceID, path: path}, nil
}

func (c *NativeClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client == nil {
		return nil
	}
	err := c.client.Close()
	c.client = nil
	return err
}

func (c *NativeClient) collection(ctx context.Context, record memory.MemoryRecord, create bool) (chroma.Collection, error) {
	if c.client == nil {
		return nil, errors.New("MemPalace native client is closed")
	}
	if record.WorkspaceID != c.workspaceID {
		return nil, errors.New("MemPalace native collection differs from selected workspace")
	}
	name, err := CollectionName(record)
	if err != nil {
		return nil, err
	}
	if create {
		return c.client.GetOrCreateCollection(ctx, name, chroma.WithHNSWSpaceCreate(embeddings.COSINE),
			chroma.WithEmbeddingFunctionCreate(suppliedVectors{}), chroma.WithDisableEFConfigStorage())
	}
	return c.client.GetCollection(ctx, name, chroma.WithEmbeddingFunctionGet(suppliedVectors{}))
}

func (c *NativeClient) Get(ctx context.Context, record memory.MemoryRecord, ids []string) ([]NativeRow, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(ids) == 0 {
		return []NativeRow{}, nil
	}
	collection, err := c.collection(ctx, record, false)
	if err != nil {
		return nil, err
	}
	lookup := make([]chroma.DocumentID, len(ids))
	for i, id := range ids {
		lookup[i] = chroma.DocumentID(id)
	}
	result, err := collection.Get(ctx, chroma.WithIDs(lookup...),
		chroma.WithInclude(chroma.IncludeDocuments, chroma.IncludeMetadatas, chroma.IncludeEmbeddings))
	if err != nil {
		return nil, err
	}
	rows := make([]NativeRow, 0, len(result.GetIDs()))
	for i, id := range result.GetIDs() {
		row := NativeRow{ID: string(id)}
		if i < len(result.GetDocuments()) && result.GetDocuments()[i] != nil {
			row.Document = result.GetDocuments()[i].ContentString()
		}
		if i < len(result.GetMetadatas()) && result.GetMetadatas()[i] != nil {
			row.Metadata, err = metadataMap(result.GetMetadatas()[i])
			if err != nil {
				return nil, err
			}
		}
		if i < len(result.GetEmbeddings()) && result.GetEmbeddings()[i] != nil {
			row.Embedding = append([]float32(nil), result.GetEmbeddings()[i].ContentAsFloat32()...)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func nativeWindowIDs(ctx context.Context, collection chroma.Collection, id, workspaceID string) ([]chroma.DocumentID, error) {
	var result []chroma.DocumentID
	for {
		found, err := collection.Get(ctx,
			chroma.WithWhere(chroma.EqString("canonical_record_id", id)),
			chroma.WithLimit(1000), chroma.WithOffset(len(result)),
			chroma.WithInclude(chroma.IncludeMetadatas))
		if err != nil {
			return nil, err
		}
		for i, windowID := range found.GetIDs() {
			if !strings.HasPrefix(string(windowID), id+":window:") ||
				i >= len(found.GetMetadatas()) || found.GetMetadatas()[i] == nil {
				return nil, errors.New("MemPalace derived window identity is invalid")
			}
			metadata, err := metadataMap(found.GetMetadatas()[i])
			if err != nil {
				return nil, err
			}
			if metadata["workspace_id"] != workspaceID || metadata["canonical_record_id"] != id {
				return nil, errors.New("MemPalace derived window workspace differs")
			}
			result = append(result, windowID)
		}
		if len(found.GetIDs()) < 1000 {
			return result, nil
		}
	}
}

// PurgeExact removes every native placement of one canonical revision,
// including published and unpublished topology copies and derived windows.
// The canonical sidecar remains.
func (c *NativeClient) PurgeExact(ctx context.Context, record memory.MemoryRecord,
	allowNativeAbsent bool) (PurgeResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := strings.TrimSpace(record.BlockID)
	if id == "" || id != record.BlockID || record.WorkspaceID != c.workspaceID {
		return PurgeResult{Error: "native_record_workspace_mismatch"}, nil
	}
	if _, err := CollectionName(record); err != nil {
		return PurgeResult{}, err
	}
	type placement struct {
		collection chroma.Collection
		canonical  bool
		windows    []chroma.DocumentID
	}
	placements := make([]placement, 0)
	canonicalFound := false
	for offset := 0; ; offset += 1000 {
		collections, err := c.client.ListCollections(ctx, chroma.ListWithLimit(1000), chroma.ListWithOffset(offset))
		if err != nil {
			return PurgeResult{}, err
		}
		for _, collection := range collections {
			found, err := collection.Get(ctx, chroma.WithIDs(chroma.DocumentID(id)),
				chroma.WithInclude(chroma.IncludeMetadatas))
			if err != nil {
				return PurgeResult{}, err
			}
			windows, err := nativeWindowIDs(ctx, collection, id, c.workspaceID)
			if err != nil {
				return PurgeResult{}, err
			}
			if len(found.GetIDs()) == 0 && len(windows) == 0 {
				continue
			}
			if len(found.GetIDs()) > 0 {
				if len(found.GetIDs()) != 1 || string(found.GetIDs()[0]) != id ||
					len(found.GetMetadatas()) != 1 || found.GetMetadatas()[0] == nil {
					return PurgeResult{Error: "native_record_workspace_mismatch"}, nil
				}
				metadata, err := metadataMap(found.GetMetadatas()[0])
				if err != nil {
					return PurgeResult{}, err
				}
				if metadata["workspace_id"] != c.workspaceID {
					return PurgeResult{Error: "native_record_workspace_mismatch"}, nil
				}
				canonicalFound = true
			}
			placements = append(placements, placement{collection: collection,
				canonical: len(found.GetIDs()) == 1, windows: windows})
		}
		if len(collections) < 1000 {
			break
		}
	}
	if !canonicalFound && !allowNativeAbsent {
		return PurgeResult{Error: "native_exact_id_not_found"}, nil
	}
	for _, place := range placements {
		if len(place.windows) > 0 {
			if err := place.collection.Delete(ctx, chroma.WithIDsDelete(place.windows...)); err != nil {
				return PurgeResult{}, err
			}
		}
		if place.canonical {
			if err := place.collection.Delete(ctx, chroma.WithIDsDelete(chroma.DocumentID(id))); err != nil {
				return PurgeResult{}, err
			}
		}
		verified, err := place.collection.Get(ctx, chroma.WithIDs(chroma.DocumentID(id)))
		if err != nil {
			return PurgeResult{}, err
		}
		remaining, err := nativeWindowIDs(ctx, place.collection, id, c.workspaceID)
		if err != nil {
			return PurgeResult{}, err
		}
		if len(verified.GetIDs()) != 0 || len(remaining) != 0 {
			return PurgeResult{Error: "native_exact_id_not_deleted"}, nil
		}
	}
	return PurgeResult{OK: true, NativeExactIDAbsent: true, NativeEdgesAbsent: true}, nil
}

func (c *NativeClient) EnsureCollection(ctx context.Context, record memory.MemoryRecord) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := c.collection(ctx, record, true)
	return err
}

func metadataMap(metadata chroma.DocumentMetadata) (map[string]any, error) {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func (c *NativeClient) Add(ctx context.Context, record memory.MemoryRecord, rows []NativeRow) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(rows) == 0 {
		return nil
	}
	collection, err := c.collection(ctx, record, true)
	if err != nil {
		return err
	}
	ids := make([]chroma.DocumentID, len(rows))
	texts := make([]string, len(rows))
	metadatas := make([]chroma.DocumentMetadata, len(rows))
	vectors := make([]embeddings.Embedding, len(rows))
	for i, row := range rows {
		if row.ID == "" || len(row.Embedding) != 384 {
			return errors.New("MemPalace native row requires ID and 384-dimensional embedding")
		}
		ids[i] = chroma.DocumentID(row.ID)
		texts[i] = row.Document
		metadatas[i], err = chroma.NewDocumentMetadataFromMap(row.Metadata)
		if err != nil {
			return fmt.Errorf("MemPalace native metadata: %w", err)
		}
		vectors[i] = embeddings.NewEmbeddingFromFloat32(row.Embedding)
	}
	return collection.Add(ctx, chroma.WithIDs(ids...), chroma.WithTexts(texts...),
		chroma.WithMetadatas(metadatas...), chroma.WithEmbeddings(vectors...))
}

func (c *NativeClient) Query(ctx context.Context, record memory.MemoryRecord, vector []float32, limit int) ([]NativeHit, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(vector) != 384 || limit < 1 || limit > 10000 {
		return nil, errors.New("MemPalace native query requires a 384-dimensional vector and bounded limit")
	}
	collection, err := c.collection(ctx, record, false)
	if err != nil {
		return nil, err
	}
	count, err := collection.Count(ctx)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return []NativeHit{}, nil
	}
	result, err := collection.Query(ctx,
		chroma.WithQueryEmbeddings(embeddings.NewEmbeddingFromFloat32(vector)),
		chroma.WithNResults(min(limit, count)),
		chroma.WithIncludeQuery(chroma.IncludeDocuments, chroma.IncludeMetadatas, chroma.IncludeDistances))
	if err != nil {
		return nil, err
	}
	if len(result.GetIDGroups()) != 1 || len(result.GetDocumentsGroups()) != 1 ||
		len(result.GetMetadatasGroups()) != 1 || len(result.GetDistancesGroups()) != 1 {
		return nil, errors.New("MemPalace native query returned an invalid shape")
	}
	ids := result.GetIDGroups()[0]
	texts := result.GetDocumentsGroups()[0]
	metadatas := result.GetMetadatasGroups()[0]
	distances := result.GetDistancesGroups()[0]
	if len(ids) != len(texts) || len(ids) != len(metadatas) || len(ids) != len(distances) {
		return nil, errors.New("MemPalace native query returned misaligned rows")
	}
	hits := make([]NativeHit, 0, len(ids))
	for i, id := range ids {
		distance := float64(distances[i])
		if math.IsNaN(distance) || math.IsInf(distance, 0) {
			return nil, errors.New("MemPalace native distance is not finite")
		}
		metadata, err := metadataMap(metadatas[i])
		if err != nil {
			return nil, err
		}
		hits = append(hits, NativeHit{NativeRow: NativeRow{ID: string(id),
			Document: texts[i].ContentString(), Metadata: metadata}, Distance: distance})
	}
	return hits, nil
}

// Lexical uses Chroma's own trigram FTS table, then retrieves the persisted
// document and metadata from the same collection. Terms are quoted as literals.
func (c *NativeClient) Lexical(ctx context.Context, record memory.MemoryRecord, query string, limit int) ([]NativeRow, error) {
	if limit < 1 || limit > 10000 {
		return nil, errors.New("MemPalace lexical query requires a bounded limit")
	}
	terms := make([]string, 0)
	seen := make(map[string]bool)
	for _, term := range strings.Fields(query) {
		if utf8.RuneCountInString(term) < 3 || seen[term] {
			continue
		}
		seen[term] = true
		terms = append(terms, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
	}
	if len(terms) == 0 {
		return []NativeRow{}, nil
	}
	name, err := CollectionName(record)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(c.path, "chroma.sqlite3"))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		return nil, err
	}
	selected, err := db.QueryContext(ctx, `SELECT e.embedding_id FROM embedding_fulltext_search
		JOIN embeddings e ON e.id = embedding_fulltext_search.rowid
		JOIN segments s ON e.segment_id = s.id
		JOIN collections c ON s.collection = c.id
		WHERE embedding_fulltext_search MATCH ? AND c.name = ?
		ORDER BY bm25(embedding_fulltext_search), e.embedding_id LIMIT ?`,
		strings.Join(terms, " OR "), name, limit)
	if err != nil {
		return nil, fmt.Errorf("MemPalace fulltext index failed: %w", err)
	}
	ids := make([]string, 0)
	for selected.Next() {
		var id string
		if err := selected.Scan(&id); err != nil {
			selected.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := selected.Err(); err != nil {
		selected.Close()
		return nil, err
	}
	if err := selected.Close(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []NativeRow{}, nil
	}
	rows, err := c.Get(ctx, record, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]NativeRow, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}
	ordered := make([]NativeRow, 0, len(ids))
	for _, id := range ids {
		row, found := byID[id]
		if !found {
			return nil, errors.New("MemPalace fulltext projection is missing")
		}
		ordered = append(ordered, row)
	}
	return ordered, nil
}
