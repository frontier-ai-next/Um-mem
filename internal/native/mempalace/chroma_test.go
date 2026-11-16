package mempalace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestNativeReadDoesNotCreateMissingCollection(t *testing.T) {
	ctx := context.Background()
	client, err := OpenNativeClient(t.TempDir(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	record := memory.MemoryRecord{WorkspaceID: "alpha", BlockMembership: &memory.RecordBlockMembership{
		PrimaryBlockID: "facts", CatalogRevisionID: "cat:v1", BackendPartitionID: "static:unit",
	}}
	if _, err := client.Get(ctx, record, []string{"missing"}); err == nil {
		t.Fatal("native read accepted a missing collection")
	}
	if _, err := client.collection(ctx, record, false); err == nil {
		t.Fatal("native read created the missing collection")
	}
}

func TestPurgeExactNativeArtifactsVerifiesWorkspaceAndAbsence(t *testing.T) {
	ctx := context.Background()
	client, err := OpenNativeClient(t.TempDir(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	record := memory.MemoryRecord{BlockID: "mem_one", WorkspaceID: "alpha",
		BlockMembership: &memory.RecordBlockMembership{PrimaryBlockID: "facts",
			CatalogRevisionID: "cat:v1", BackendPartitionID: "static:unit"}}
	vector := make([]float32, 384)
	vector[0] = 1
	windowID := record.BlockID + ":window:0000"
	neighborID := record.BlockID + "_neighbor"
	if err := client.Add(ctx, record, []NativeRow{{ID: record.BlockID,
		Document: "private native text", Metadata: map[string]any{"workspace_id": "alpha"},
		Embedding: vector}, {ID: windowID, Document: "private window text",
		Metadata:  map[string]any{"workspace_id": "alpha", "canonical_record_id": record.BlockID},
		Embedding: vector}, {ID: neighborID, Document: "neighbor text",
		Metadata:  map[string]any{"workspace_id": "alpha", "canonical_record_id": neighborID},
		Embedding: vector}}); err != nil {
		t.Fatal(err)
	}
	foreign := record
	foreign.WorkspaceID = "other"
	if result, err := client.PurgeExact(ctx, foreign, false); err != nil || result.OK ||
		result.Error != "native_record_workspace_mismatch" {
		t.Fatalf("foreign workspace purge was accepted: %+v %v", result, err)
	}
	result, err := client.PurgeExact(ctx, record, false)
	if err != nil || !result.OK || !result.NativeExactIDAbsent || !result.NativeEdgesAbsent {
		t.Fatalf("native purge did not verify exact absence: %+v %v", result, err)
	}
	rows, err := client.Get(ctx, record, []string{record.BlockID, windowID, neighborID})
	if err != nil || len(rows) != 1 || rows[0].ID != neighborID {
		t.Fatalf("purged native ID remained readable: %+v %v", rows, err)
	}
	if result, err := client.PurgeExact(ctx, record, false); err != nil || result.OK ||
		result.Error != "native_exact_id_not_found" {
		t.Fatalf("fresh purge accepted absent native ID: %+v %v", result, err)
	}
	if result, err := client.PurgeExact(ctx, record, true); err != nil || !result.OK {
		t.Fatalf("resumed purge could not verify absence: %+v %v", result, err)
	}
}

func TestPurgeExactRemovesPublishedAndStagedPlacements(t *testing.T) {
	ctx := context.Background()
	client, err := OpenNativeClient(t.TempDir(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	base := memory.MemoryRecord{BlockID: "mem_topology", WorkspaceID: "alpha",
		BlockMembership: &memory.RecordBlockMembership{PrimaryBlockID: "source",
			CatalogRevisionID: "cat:v1", BackendPartitionID: "static:source"}}
	staged := base
	copyMembership := *base.BlockMembership
	copyMembership.PrimaryBlockID = "target"
	copyMembership.BackendPartitionID = "dynamic:target"
	copyMembership.CatalogRevisionID = "topology:split-one"
	staged.BlockMembership = &copyMembership
	vector := make([]float32, 384)
	vector[0] = 1
	for _, record := range []memory.MemoryRecord{base, staged} {
		if err := client.Add(ctx, record, []NativeRow{{ID: record.BlockID,
			Document: "private topology text", Metadata: map[string]any{"workspace_id": "alpha"},
			Embedding: vector}, {ID: record.BlockID + ":window:0000",
			Document: "private topology window", Metadata: map[string]any{
				"workspace_id": "alpha", "canonical_record_id": record.BlockID}, Embedding: vector}}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := client.PurgeExact(ctx, staged, false)
	if err != nil || !result.OK {
		t.Fatalf("multi-placement purge failed: %+v %v", result, err)
	}
	for _, record := range []memory.MemoryRecord{base, staged} {
		rows, err := client.Get(ctx, record, []string{record.BlockID, record.BlockID + ":window:0000"})
		if err != nil || len(rows) != 0 {
			t.Fatalf("native topology placement survived purge: %+v %v", rows, err)
		}
	}
}

// UM_MEM_TEST_PYTHON_PALACE_ROOT points at a disposable Python-created palace.
// The fixture is kept outside the repository because its HNSW files are native.
func TestReadPythonPalace(t *testing.T) {
	root := os.Getenv("UM_MEM_TEST_PYTHON_PALACE_ROOT")
	if root == "" {
		t.Skip("Python-created palace fixture is unavailable")
	}
	ctx := context.Background()
	indexPath := filepath.Join(root, "workspaces", "alpha", ".memory", "mempalace", "UM_MEM_index.sqlite")
	index, err := store.OpenIndex(indexPath, "alpha", "default")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	record, err := index.ReadStoredRecord(ctx, "mem_c3a6452e2bd27eca")
	if err != nil || record == nil {
		t.Fatalf("read Python canonical record: %v, %v", record, err)
	}
	projections, err := index.ProjectionRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var projected *store.ProjectionRow
	for i := range projections {
		if projections[i].Record.BlockID == record.BlockID {
			projected = &projections[i]
		}
	}
	if projected == nil || projected.Backend != StoreForBlock(*record) ||
		projected.NativeID != record.BlockID || projected.Status != "committed" {
		t.Fatalf("Python write journal differs from Go MemPalace contract: %+v", projected)
	}
	client, err := OpenNativeClient(root, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	rows, err := client.Get(ctx, *record, []string{record.BlockID})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != record.BlockID || rows[0].Document != record.Content || len(rows[0].Embedding) != 384 {
		t.Fatalf("Python native row does not match canonical source: %+v", rows)
	}
	if rows[0].Metadata["canonical_record_json"] == nil {
		t.Fatalf("Python native metadata is missing canonical payload: %+v", rows[0].Metadata)
	}
	hits, err := client.Query(ctx, *record, rows[0].Embedding, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != record.BlockID {
		t.Fatalf("Python native query did not find the existing record: %+v", hits)
	}
	lexical, err := client.Lexical(ctx, *record, "blue comet", 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range lexical {
		found = found || row.ID == record.BlockID
	}
	if !found {
		t.Fatalf("Python native FTS did not find the existing record: %+v", lexical)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if modelDir := os.Getenv("UM_MEM_TEST_MINILM_DIR"); modelDir != "" {
		projector, err := NewProjector(ctx, root, modelDir)
		if err != nil {
			t.Fatal(err)
		}
		defer projector.Close()
		plan := memory.NewRecallPlan("blue comet protocol")
		plan.WorkspaceID, plan.ProfileID = "alpha", "default"
		plan.SearchAll = true
		plan.ScopeChain = []string{"workspace:alpha"}
		plan.IncludeCandidates = true
		for _, mode := range []string{"semantic", "hybrid"} {
			plan.RetrievalMode = mode
			hits, err := projector.Recall(ctx, index, plan)
			if err != nil {
				t.Fatalf("Go %s recall of Python palace failed: %v", mode, err)
			}
			found := false
			for _, hit := range hits {
				found = found || hit.BlockID == record.BlockID
			}
			if !found {
				t.Fatalf("Go %s recall missed Python palace record: %+v", mode, hits)
			}
		}
	}
}

func TestGoProjectionAndReplay(t *testing.T) {
	modelDir := os.Getenv("UM_MEM_TEST_MINILM_DIR")
	if modelDir == "" {
		t.Skip("MiniLM model fixture is unavailable")
	}
	ctx := context.Background()
	root := os.Getenv("UM_MEM_TEST_GO_PALACE_ROOT")
	if root == "" {
		root = t.TempDir()
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "memory", "testdata", "record_bound_v9.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(data)
	if err != nil {
		t.Fatal(err)
	}
	projector, err := NewProjector(ctx, root, modelDir)
	if err != nil {
		t.Fatal(err)
	}
	defer projector.Close()
	payload, err := jsoncompat.MarshalPython(record, false)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := projector.Project(ctx, record, string(payload), 7)
		if err != nil || result.NativeID != record.BlockID || !result.Verified {
			t.Fatalf("MemPalace projection or replay failed: %+v, %v", result, err)
		}
	}
	client, err := OpenNativeClient(root, record.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := client.Get(ctx, record, []string{record.BlockID})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Document != record.Content || len(rows[0].Embedding) != 384 {
		t.Fatalf("Go MemPalace row is incomplete: %+v", rows)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	record.Content = "A conflicting replacement"
	conflictPayload, err := jsoncompat.MarshalPython(record, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projector.Project(ctx, record, string(conflictPayload), 7); err == nil {
		t.Fatal("MemPalace replay accepted a conflicting immutable record")
	}
}
