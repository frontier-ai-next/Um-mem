package mempalace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/native/zvec"
	"github.com/frontier-ai-next/Um-mem/internal/paths"
	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
	"github.com/gofrs/flock"
	"github.com/gomlx/go-huggingface/tokenizers/hftokenizer"
)

type Projector struct {
	RepoRoot  string
	Embedder  *zvec.MiniLMEmbedder
	tokenizer *hftokenizer.Tokenizer
}

func NewProjector(ctx context.Context, repoRoot, modelDir string) (*Projector, error) {
	if repoRoot == "" {
		return nil, errors.New("MemPalace projector requires repository root")
	}
	modelDir, err := zvec.EnsureMiniLMModel(ctx, modelDir)
	if err != nil {
		return nil, err
	}
	tokenizer, err := openTokenizer(modelDir)
	if err != nil {
		return nil, err
	}
	embedder, err := zvec.OpenMiniLMEmbedder(ctx, modelDir)
	if err != nil {
		return nil, err
	}
	return &Projector{RepoRoot: repoRoot, Embedder: embedder, tokenizer: tokenizer}, nil
}

func (*Projector) BackendID() string { return "mempalace" }

func (*Projector) PartitionIdentity(record memory.MemoryRecord) (memory.NativePartitionIdentity, error) {
	identity, err := Partition(record)
	return memory.NativePartitionIdentity(identity), err
}

func (p *Projector) Probe(context.Context) error {
	if p == nil || p.Embedder == nil || p.tokenizer == nil {
		return errors.New("MemPalace native model is unavailable")
	}
	return nil
}

// ProbeWorkspace uses the same palace operation gate as native writes so a
// health read cannot race a collection update in another process.
func (p *Projector) ProbeWorkspace(ctx context.Context, workspaceID string) error {
	if err := p.Probe(ctx); err != nil {
		return err
	}
	lockPath, err := operationLockPath(p.RepoRoot, workspaceID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return err
	}
	lock := flock.New(lockPath)
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(probeCtx, 50*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return errors.New("MemPalace native health lock timed out")
	}
	defer lock.Unlock()
	client, err := OpenNativeClient(p.RepoRoot, workspaceID)
	if err != nil {
		return err
	}
	return client.Close()
}

func (*Projector) RequiresNative(memory.MemoryRecord) bool { return true }

func operationLockPath(repoRoot, workspaceID string) (string, error) {
	root, err := paths.WorkspaceMemoryRoot(repoRoot, workspaceID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "mempalace", "UM_MEM_index.operation.lock"), nil
}

// PurgeExactNative serializes an authorized erase with native projection in
// this workspace. Authorization and sidecar lifecycle remain engine-owned.
func PurgeExactNative(ctx context.Context, repoRoot string, record memory.MemoryRecord,
	allowNativeAbsent bool) (PurgeResult, error) {
	lockPath, err := operationLockPath(repoRoot, record.WorkspaceID)
	if err != nil {
		return PurgeResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return PurgeResult{}, err
	}
	lock := flock.New(lockPath)
	lockCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 50*time.Millisecond)
	if err != nil {
		return PurgeResult{}, err
	}
	if !locked {
		return PurgeResult{}, errors.New("MemPalace native operation lock timed out")
	}
	defer lock.Unlock()
	client, err := OpenNativeClient(repoRoot, record.WorkspaceID)
	if err != nil {
		return PurgeResult{}, err
	}
	result, purgeErr := client.PurgeExact(ctx, record, allowNativeAbsent)
	closeErr := client.Close()
	if purgeErr != nil {
		return PurgeResult{}, purgeErr
	}
	if closeErr != nil {
		return PurgeResult{}, closeErr
	}
	return result, nil
}

func (p *Projector) Project(ctx context.Context, record memory.MemoryRecord, payload string, revision int64) (store.ProjectionResult, error) {
	if p == nil || p.Embedder == nil || p.tokenizer == nil || revision < 1 {
		return store.ProjectionResult{}, errors.New("MemPalace projection requires model, tokenizer and commit revision")
	}
	decoded, err := memory.DecodeCanonicalRecord([]byte(payload))
	if err != nil || !reflect.DeepEqual(decoded, record) {
		return store.ProjectionResult{}, errors.New("MemPalace projection payload differs from canonical record")
	}
	rows, err := NativeRows(record, revision, p.tokenizer)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	lockPath, err := operationLockPath(p.RepoRoot, record.WorkspaceID)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return store.ProjectionResult{}, err
	}
	lock := flock.New(lockPath)
	lockCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 50*time.Millisecond)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	if !locked {
		return store.ProjectionResult{}, errors.New("MemPalace native operation lock timed out")
	}
	defer lock.Unlock()
	client, err := OpenNativeClient(p.RepoRoot, record.WorkspaceID)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	projectErr := p.projectRows(ctx, client, record, rows)
	closeErr := client.Close()
	if projectErr != nil {
		return store.ProjectionResult{}, projectErr
	}
	if closeErr != nil {
		return store.ProjectionResult{}, closeErr
	}
	return store.ProjectionResult{NativeID: record.BlockID, Verified: true}, nil
}

func (p *Projector) projectRows(ctx context.Context, client *NativeClient, record memory.MemoryRecord, expected []NativeRow) error {
	if err := client.EnsureCollection(ctx, record); err != nil {
		return err
	}
	ids := make([]string, len(expected))
	for i, row := range expected {
		ids[i] = row.ID
	}
	present, err := client.Get(ctx, record, ids)
	if err != nil {
		return err
	}
	byID := make(map[string]NativeRow, len(present))
	for _, row := range present {
		byID[row.ID] = row
	}
	missing := make([]NativeRow, 0, len(expected))
	for _, row := range expected {
		actual, found := byID[row.ID]
		if !found {
			missing = append(missing, row)
			continue
		}
		if err := verifyNativeRow(actual, row); err != nil {
			return err
		}
	}
	if len(missing) > 0 {
		texts := make([]string, len(missing))
		for i, row := range missing {
			texts[i] = row.Document
			if row.ID == record.BlockID && len(expected) > 1 {
				spans := p.tokenizer.EncodeWithAnnotations(row.Document).Spans
				contentTokens := 0
				for _, span := range spans {
					if span.End <= span.Start {
						continue
					}
					contentTokens++
					if contentTokens == 254 {
						texts[i] = row.Document[:span.End]
						break
					}
				}
			}
		}
		vectors, err := p.Embedder.Embed(ctx, texts)
		if err != nil {
			return err
		}
		for i := range missing {
			missing[i].Embedding = vectors[i]
		}
		if err := client.Add(ctx, record, missing); err != nil {
			return err
		}
	}
	verified, err := client.Get(ctx, record, ids)
	if err != nil {
		return err
	}
	if len(verified) != len(expected) {
		return errors.New("MemPalace native projection is incomplete")
	}
	byID = make(map[string]NativeRow, len(verified))
	for _, row := range verified {
		byID[row.ID] = row
	}
	for _, row := range expected {
		actual, found := byID[row.ID]
		if !found {
			return errors.New("MemPalace native projection row is missing")
		}
		if err := verifyNativeRow(actual, row); err != nil {
			return err
		}
	}
	return nil
}

func verifyNativeRow(actual, expected NativeRow) error {
	if actual.ID != expected.ID || actual.Document != expected.Document ||
		len(actual.Embedding) != 384 || !sameNativeMetadata(actual.Metadata, expected.Metadata) {
		return fmt.Errorf("MemPalace native projection row %q conflicts with canonical source", expected.ID)
	}
	if strings.Contains(expected.ID, ":window:") {
		return nil
	}
	record, err := nativeRecordJSON(actual.Metadata)
	if err != nil || record.BlockID != expected.ID || record.Content != expected.Document {
		return errors.New("MemPalace native canonical payload is invalid")
	}
	return nil
}

func (p *Projector) Close() error {
	if p == nil || p.Embedder == nil {
		return nil
	}
	err := p.Embedder.Close()
	p.Embedder = nil
	return err
}
