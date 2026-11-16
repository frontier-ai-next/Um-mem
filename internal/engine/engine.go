package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/frontier-ai-next/Um-mem/internal/blockcatalog"
	"github.com/frontier-ai-next/Um-mem/internal/catalog"
	"github.com/frontier-ai-next/Um-mem/internal/native/hindsight"
	"github.com/frontier-ai-next/Um-mem/internal/native/mempalace"
	"github.com/frontier-ai-next/Um-mem/internal/native/mnemosyne"
	"github.com/frontier-ai-next/Um-mem/internal/paths"
	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/internal/telemetry"
	"github.com/frontier-ai-next/Um-mem/memory"
)

type Config struct {
	RepoRoot         string
	WorkspaceID      string
	ProfileID        string
	BackendID        string
	HindsightBaseURL string
	HindsightAPIKey  string
	MiniLMModelDir   string
	ZvecModelDir     string
}

type Engine struct {
	config    Config
	indexPath string
	refreshMu sync.Mutex
	current   atomic.Pointer[catalogSnapshot]
	index     *store.Index
	topology  *catalog.Store
	writer    *store.Writer
	projector store.NativeProjector
	recaller  nativeRecaller
}

type catalogSnapshot struct {
	blocks  []memory.Block
	byID    map[string]memory.Block
	catalog memory.SemanticCatalog
}

func (engine *Engine) Scope() (workspaceID, profileID string) {
	return engine.config.WorkspaceID, engine.config.ProfileID
}

func (engine *Engine) BackendID() string { return engine.config.BackendID }
func (engine *Engine) RepoRoot() string  { return engine.config.RepoRoot }

// RecordIDsForLogicalBlocks resolves exact current primary membership through
// the canonical index and any published topology generation.
func (engine *Engine) RecordIDsForLogicalBlocks(ctx context.Context, blockIDs []string) ([]string, error) {
	return engine.index.RecordIDsForLogicalBlocks(ctx, blockIDs)
}

type nativeRecaller interface {
	Recall(context.Context, *store.Index, memory.RecallPlan) ([]memory.MemoryRecord, error)
}

func (config Config) miniLMDir() string {
	if config.MiniLMModelDir != "" {
		return config.MiniLMModelDir
	}
	return config.ZvecModelDir
}

func IndexPath(config Config) (string, error) {
	root, err := paths.WorkspaceMemoryRoot(config.RepoRoot, config.WorkspaceID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, config.BackendID, "UM_MEM_index.sqlite"), nil
}

// Open binds a workspace/profile to a single durable backend. Only a backend
// with a verified Go projection and recall path may be selected here.
func Open(config Config) (*Engine, error) {
	config.WorkspaceID = strings.TrimSpace(config.WorkspaceID)
	config.ProfileID = strings.TrimSpace(config.ProfileID)
	if config.WorkspaceID == "" || config.ProfileID == "" || strings.TrimSpace(config.RepoRoot) == "" {
		return nil, errors.New("engine requires repo root, workspace and profile")
	}
	if config.BackendID != "hindsight" && config.BackendID != "mnemosyne" && config.BackendID != "zvec" && config.BackendID != "mempalace" {
		return nil, fmt.Errorf("Go native backend %q is not implemented", config.BackendID)
	}
	blocks, err := blockcatalog.Load(blockcatalog.Options{RepoRoot: config.RepoRoot,
		WorkspaceID: config.WorkspaceID, ProfileID: config.ProfileID,
		Strict: true, IncludeWorkspace: true})
	if err != nil {
		return nil, err
	}
	semantic, err := memory.CompileSemanticCatalog(blocks)
	if err != nil {
		return nil, err
	}
	indexPath, err := IndexPath(config)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		if err := store.CreateIndex(indexPath); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	writer, err := store.OpenWriter(indexPath, config.WorkspaceID, config.ProfileID, config.BackendID)
	if err != nil {
		return nil, err
	}
	index, err := store.OpenIndex(indexPath, config.WorkspaceID, config.ProfileID)
	if err != nil {
		writer.Close()
		return nil, err
	}
	var projector store.NativeProjector
	var recaller nativeRecaller
	if config.BackendID == "hindsight" {
		if config.HindsightBaseURL == "" {
			config.HindsightBaseURL = "http://localhost:8888"
		}
		native := hindsight.Projector{BaseURL: config.HindsightBaseURL, APIKey: config.HindsightAPIKey}
		projector, recaller = native, native
	} else if config.BackendID == "mnemosyne" {
		native, err := mnemosyne.NewProjector(config.RepoRoot, config.WorkspaceID)
		if err != nil {
			index.Close()
			writer.Close()
			return nil, err
		}
		projector, recaller = native, native
	} else if config.BackendID == "mempalace" {
		native, openErr := mempalace.NewProjector(context.Background(), config.RepoRoot, config.miniLMDir())
		if openErr != nil {
			index.Close()
			writer.Close()
			return nil, openErr
		}
		projector, recaller = native, native
	} else {
		projector, recaller, err = openZvecBackend(config)
		if err != nil {
			index.Close()
			writer.Close()
			return nil, err
		}
	}
	topology, err := catalog.Open(config.RepoRoot, config.WorkspaceID, config.ProfileID)
	if err != nil {
		if closer, ok := projector.(interface{ Close() error }); ok {
			closer.Close()
		}
		index.Close()
		writer.Close()
		return nil, err
	}
	index.BindTopologyCatalog(topology)
	index.BindPartitionMetricsWriter(writer)
	byID := make(map[string]memory.Block, len(blocks))
	for _, block := range blocks {
		byID[block.BlockID] = block
	}
	selected := &Engine{config: config, indexPath: indexPath,
		index: index, topology: topology, writer: writer, projector: projector, recaller: recaller}
	selected.current.Store(&catalogSnapshot{blocks: blocks, byID: byID, catalog: semantic})
	return selected, nil
}

func (engine *Engine) Close() error {
	indexErr := engine.index.Close()
	topologyErr := engine.topology.Close()
	writerErr := engine.writer.Close()
	var nativeErr error
	if closer, ok := engine.projector.(interface{ Close() error }); ok {
		nativeErr = closer.Close()
	}
	return errors.Join(indexErr, topologyErr, writerErr, nativeErr)
}

func (engine *Engine) IndexPath() string { return engine.indexPath }

func (engine *Engine) Blocks() []memory.Block {
	return append([]memory.Block(nil), engine.current.Load().blocks...)
}

func (engine *Engine) CatalogRevisionID() string { return engine.current.Load().catalog.RevisionID }

// RefreshCatalog atomically publishes the latest validated block topology so
// existing MCP and service processes can write to blocks created after Open.
func (engine *Engine) RefreshCatalog() error {
	engine.refreshMu.Lock()
	defer engine.refreshMu.Unlock()
	blocks, err := blockcatalog.Load(blockcatalog.Options{RepoRoot: engine.config.RepoRoot,
		WorkspaceID: engine.config.WorkspaceID, ProfileID: engine.config.ProfileID,
		Strict: true, IncludeWorkspace: true})
	if err != nil {
		return err
	}
	semantic, err := memory.CompileSemanticCatalog(blocks)
	if err != nil {
		return err
	}
	byID := make(map[string]memory.Block, len(blocks))
	for _, block := range blocks {
		byID[block.BlockID] = block
	}
	engine.current.Store(&catalogSnapshot{blocks: blocks, byID: byID, catalog: semantic})
	return nil
}

func (engine *Engine) blockPartition(ctx context.Context, blockID string) (string, error) {
	block, exists := engine.current.Load().byID[blockID]
	if !exists || block.IndexOnly || !block.AgentWritable {
		return "", errors.New("memory write requires a writable content block")
	}
	store, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return "", err
	}
	defer store.Close()
	state, err := store.GetBlock(ctx, blockID)
	if err != nil {
		return "", err
	}
	if state != nil {
		if state.Status != "active" || state.BackendPartitionID == "" {
			return "", errors.New("dynamic content block is archived or has no native partition")
		}
		return state.BackendPartitionID, nil
	}
	return memory.StaticBackendPartitionID(engine.config.ProfileID, blockID), nil
}

// BindRootRecord assigns the exact current catalog and physical partition to
// an unbound canonical root record before it enters the write journal.
func (engine *Engine) BindRootRecord(ctx context.Context, record *memory.MemoryRecord, blockID string) error {
	if record == nil || record.RevisionOf != "" || len(record.SupersedesIDs) > 0 {
		return errors.New("BindRootRecord requires a new root revision")
	}
	if err := engine.RefreshCatalog(); err != nil {
		return err
	}
	return engine.bindRecord(ctx, record, blockID)
}

func (engine *Engine) bindRecord(ctx context.Context, record *memory.MemoryRecord, blockID string) error {
	if record == nil || record.BlockMembership != nil {
		return errors.New("record binding requires an unbound record")
	}
	if record.WorkspaceID != engine.config.WorkspaceID || record.ProfileID != engine.config.ProfileID {
		return errors.New("record identity differs from selected engine scope")
	}
	current := engine.current.Load()
	block, exists := current.byID[blockID]
	if !exists || block.Scope != record.Scope {
		return errors.New("selected block does not match record scope")
	}
	partition, err := engine.blockPartition(ctx, blockID)
	if err != nil {
		return err
	}
	membership := memory.RecordBlockMembership{PrimaryBlockID: blockID,
		SecondaryBlockIDs: []string{}, CatalogRevisionID: current.catalog.RevisionID,
		BackendPartitionID: partition, RecordID: record.BlockID,
		RecordRevisionID: record.RevisionID, WorkspaceID: record.WorkspaceID,
		ValidFrom: record.ValidFrom}
	membership.MembershipRevisionID = membership.RevisionHash()
	record.BlockMembership = &membership
	record.SemanticCatalogVersion = current.catalog.RevisionID
	return record.ValidateForStorage()
}

// BindSuccessorRecord pins a correction or lifecycle successor to the current
// catalog while the writer remains responsible for canonical-head CAS.
func (engine *Engine) BindSuccessorRecord(ctx context.Context, record *memory.MemoryRecord, blockID string) error {
	if record == nil || strings.TrimSpace(record.RevisionOf) == "" || record.CanonicalKey == "" {
		return errors.New("canonical successor requires predecessor and key")
	}
	predecessor, err := engine.index.ReadStoredRecord(ctx, record.RevisionOf)
	if err != nil {
		return err
	}
	if predecessor == nil || predecessor.ProfileID != engine.config.ProfileID ||
		predecessor.CanonicalKey != record.CanonicalKey || predecessor.WorkspaceID != record.WorkspaceID {
		return errors.New("canonical predecessor is outside selected key or scope")
	}
	if err := engine.RefreshCatalog(); err != nil {
		return err
	}
	return engine.bindRecord(ctx, record, blockID)
}

func (engine *Engine) validateBoundRecord(ctx context.Context, record memory.MemoryRecord) error {
	current := engine.current.Load()
	if record.BlockMembership == nil || record.BlockMembership.CatalogRevisionID != current.catalog.RevisionID {
		return errors.New("record membership does not match current semantic catalog")
	}
	block, exists := current.byID[record.BlockMembership.PrimaryBlockID]
	if !exists || block.Scope != record.Scope || record.SemanticCatalogVersion != current.catalog.RevisionID {
		return errors.New("record block scope or semantic catalog version is invalid")
	}
	for _, secondaryID := range record.BlockMembership.SecondaryBlockIDs {
		if _, exists := current.byID[secondaryID]; !exists {
			return fmt.Errorf("secondary block %q is outside selected catalog", secondaryID)
		}
	}
	partition, err := engine.blockPartition(ctx, record.BlockMembership.PrimaryBlockID)
	if err != nil {
		return err
	}
	if record.BlockMembership.BackendPartitionID != partition || record.WorkspaceID != engine.config.WorkspaceID ||
		record.ProfileID != engine.config.ProfileID {
		return errors.New("record membership partition or scope differs from selected engine")
	}
	return engine.assertChainNotErased(ctx, record.CanonicalKey)
}

func (engine *Engine) PutRoot(ctx context.Context, record memory.MemoryRecord) (store.WriteReceipt, error) {
	if err := engine.validateBoundRecord(ctx, record); err != nil {
		return store.WriteReceipt{}, err
	}
	staged, err := engine.writer.PrepareRootWrite(ctx, record)
	if err != nil {
		return store.WriteReceipt{}, err
	}
	return engine.writer.CompleteProjection(ctx, staged.CommitRevision, engine.projector)
}

func (engine *Engine) PutConsolidationRoot(ctx context.Context, record memory.MemoryRecord,
	consolidation memory.ConsolidationCommitReceipt) (store.WriteReceipt, error) {
	if err := engine.validateBoundRecord(ctx, record); err != nil {
		return store.WriteReceipt{}, err
	}
	staged, err := engine.writer.PrepareConsolidationRootWrite(ctx, record, consolidation)
	if err != nil {
		return store.WriteReceipt{}, err
	}
	return engine.writer.CompleteProjection(ctx, staged.CommitRevision, engine.projector)
}

func (engine *Engine) PutSuccessor(ctx context.Context, record memory.MemoryRecord, action, rollbackTargetID string) (store.WriteReceipt, error) {
	lock, err := engine.acquireEraseWorkspaceLock(ctx)
	if err != nil {
		return store.WriteReceipt{}, err
	}
	defer lock.Unlock()
	if err := engine.validateBoundRecord(ctx, record); err != nil {
		return store.WriteReceipt{}, err
	}
	staged, err := engine.prepareSuccessorWrite(ctx, record, action, rollbackTargetID)
	if err != nil {
		return store.WriteReceipt{}, err
	}
	return engine.writer.CompleteProjection(ctx, staged.CommitRevision, engine.projector)
}

func (engine *Engine) prepareSuccessorWrite(ctx context.Context, record memory.MemoryRecord,
	action, rollbackTargetID string) (store.WriteReceipt, error) {
	if record.BlockMembership != nil && record.RevisionOf != "" && action != "correction" {
		predecessor, err := engine.ReadStored(ctx, record.RevisionOf)
		if err != nil {
			return store.WriteReceipt{}, err
		}
		if predecessor != nil {
			pointer, err := engine.topology.PublishedRecordMembership(ctx,
				predecessor.BlockID, predecessor.RevisionID)
			if err != nil {
				return store.WriteReceipt{}, err
			}
			if pointer != nil && pointer.BlockID == record.BlockMembership.PrimaryBlockID &&
				pointer.BackendPartitionID == record.BlockMembership.BackendPartitionID {
				return engine.writer.PreparePublishedSuccessorWrite(ctx, record, action, rollbackTargetID)
			}
		}
	}
	return engine.writer.PrepareSuccessorWrite(ctx, record, action, rollbackTargetID)
}

func (engine *Engine) ReadStored(ctx context.Context, recordID string) (*memory.MemoryRecord, error) {
	return engine.index.ReadStoredRecord(ctx, recordID)
}

func (engine *Engine) ReadEvidence(ctx context.Context, evidenceRef string) (*memory.MemoryRecord, error) {
	return engine.index.ReadEvidenceRecord(ctx, evidenceRef)
}

func (engine *Engine) Recall(ctx context.Context, plan memory.RecallPlan) (records []memory.MemoryRecord, err error) {
	ctx, span := telemetry.StartProfile(ctx, engine.config.RepoRoot, telemetry.ProfileOptions{
		Operation: "backend_recall", Component: engine.config.BackendID,
		WorkspaceID: engine.config.WorkspaceID, ProfileID: engine.config.ProfileID,
		RunID: plan.RunID, Backend: engine.config.BackendID,
		Input: map[string]any{"query": plan.Query, "limit": plan.Limit,
			"logical_block_ids": plan.LogicalBlockIDs, "record_ids": plan.RecordIDs}})
	defer func() {
		if err == nil {
			span.SetOutput(map[string]any{"hit_count": len(records)})
		}
		span.End(err)
	}()
	if plan.WorkspaceID != engine.config.WorkspaceID || plan.ProfileID != engine.config.ProfileID {
		return nil, errors.New("recall scope differs from selected engine")
	}
	// `um.*` facets address record metadata, not indexed retrieval terms: keep
	// them out of term matching and apply them as an exact filter afterwards.
	metaFacets := make(map[string][]string, len(plan.Facets))
	for key, values := range plan.Facets {
		if strings.HasPrefix(key, "um.") {
			metaFacets[key] = values
			delete(plan.Facets, key)
		}
	}
	records, err = engine.recaller.Recall(ctx, engine.index, plan)
	if err != nil {
		return nil, err
	}
	if len(metaFacets) > 0 {
		records = filterMetadataFacets(records, metaFacets)
	}
	records = engine.hybridRank(ctx, plan.Query, records, plan.Limit)
	if withdrawn, werr := engine.WithdrawnIDs(ctx); werr == nil {
		records = markWithdrawn(records, withdrawn)
	}
	return records, nil
}

func filterMetadataFacets(records []memory.MemoryRecord, facets map[string][]string) []memory.MemoryRecord {
	kept := make([]memory.MemoryRecord, 0, len(records))
	for _, record := range records {
		if matchesMetadataFacets(record, facets) {
			kept = append(kept, record)
		}
	}
	return kept
}

func matchesMetadataFacets(record memory.MemoryRecord, facets map[string][]string) bool {
	for key, values := range facets {
		field := strings.TrimPrefix(key, "um.")
		actual := ""
		if record.Metadata != nil {
			if raw, found := record.Metadata[field]; found {
				actual = fmt.Sprint(raw)
			}
		}
		matched := false
		for _, value := range values {
			if value == actual {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
