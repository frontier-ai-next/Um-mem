//go:build purego

package zvec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
	"github.com/gofrs/flock"
)

type Projector struct {
	RepoRoot string
	Embedder *MiniLMEmbedder
}

func NewProjector(ctx context.Context, repoRoot, modelDir string) (*Projector, error) {
	if repoRoot == "" {
		return nil, errors.New("Zvec projector requires repository root")
	}
	modelDir, err := EnsureMiniLMModel(ctx, modelDir)
	if err != nil {
		return nil, err
	}
	embedder, err := OpenMiniLMEmbedder(ctx, modelDir)
	if err != nil {
		return nil, err
	}
	return &Projector{RepoRoot: repoRoot, Embedder: embedder}, nil
}

func (*Projector) BackendID() string { return "zvec" }

func (*Projector) PartitionIdentity(record memory.MemoryRecord) (memory.NativePartitionIdentity, error) {
	identity, err := Partition(record)
	return memory.NativePartitionIdentity(identity), err
}

func (p *Projector) Probe(ctx context.Context) error {
	if p == nil || p.Embedder == nil {
		return errors.New("Zvec native model is unavailable")
	}
	_, err := EnsureNativeLibrary(ctx)
	return err
}

func (*Projector) RequiresNative(memory.MemoryRecord) bool { return true }

func (p *Projector) Project(ctx context.Context, record memory.MemoryRecord, payload string, revision int64) (store.ProjectionResult, error) {
	if p == nil || p.Embedder == nil || revision < 1 {
		return store.ProjectionResult{}, errors.New("Zvec projection requires an embedder and commit revision")
	}
	decoded, err := memory.DecodeCanonicalRecord([]byte(payload))
	if err != nil || !reflect.DeepEqual(decoded, record) {
		return store.ProjectionResult{}, errors.New("Zvec projection payload differs from canonical record")
	}
	path, err := CollectionPath(p.RepoRoot, record)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	lockPath, err := ProjectionLockPath(p.RepoRoot, record)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return store.ProjectionResult{}, err
	}
	lock := flock.New(lockPath)
	lockCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 50*time.Millisecond)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	if !locked {
		return store.ProjectionResult{}, errors.New("Zvec projection lock timed out")
	}
	defer lock.Unlock()
	collection, err := OpenNativeCollection(path, true)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	nativeID, projectErr := collection.Project(ctx, record, revision, p.Embedder)
	closeErr := collection.Close()
	if projectErr != nil {
		return store.ProjectionResult{}, projectErr
	}
	if closeErr != nil {
		return store.ProjectionResult{}, closeErr
	}
	return store.ProjectionResult{NativeID: nativeID, Verified: true}, nil
}

func (p *Projector) Close() error {
	if p == nil || p.Embedder == nil {
		return nil
	}
	err := p.Embedder.Close()
	p.Embedder = nil
	return err
}
