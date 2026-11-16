package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestRebuildDerivedRecordIndexesRestoresRowsWithoutChangingCanonicalRecords(t *testing.T) {
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "UM_MEM_index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path, "alpha", "team", "mempalace")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx := context.Background()
	if _, err := writer.PrepareRootWrite(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.db.ExecContext(ctx, `INSERT INTO index_nodes(
		node_id,workspace_id,node_type,title,tags_json,payload_json,updated_at
	) VALUES('descriptor:existing','alpha','block_descriptor','Existing descriptor',
		'[]','{"source":"python"}','2024-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	index, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	before, err := index.MemoryIndexSHA256(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var canonicalBefore string
	if err := writer.db.QueryRowContext(ctx, `SELECT payload_json FROM blocks WHERE block_id=?`,
		record.BlockID).Scan(&canonicalBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.db.ExecContext(ctx, `INSERT INTO memory_exact_terms(
		workspace_id,term_type,term_value,block_id) VALUES('beta','path','other.go','foreign')`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"memory_exact_terms", "record_block_memberships",
		"memory_temporal_index", "memory_fts"} {
		if _, err := writer.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE workspace_id='alpha'"); err != nil {
			t.Fatal(err)
		}
	}
	corrupted, err := index.MemoryIndexSHA256(ctx)
	if err != nil || corrupted == before {
		t.Fatalf("derived corruption was not visible: %q, %v", corrupted, err)
	}
	first, err := writer.RebuildDerivedRecordIndexes(ctx)
	if err != nil || first.WorkspaceID != "alpha" || first.RecordCount != 1 || first.IndexSHA256 != before {
		t.Fatalf("derived rebuild differs: %+v, %v; baseline=%s", first, err, before)
	}
	second, err := writer.RebuildDerivedRecordIndexes(ctx)
	if err != nil || second.IndexSHA256 != first.IndexSHA256 {
		t.Fatalf("derived rebuild is not idempotent: %+v, %v", second, err)
	}
	var canonicalAfter string
	if err := writer.db.QueryRowContext(ctx, `SELECT payload_json FROM blocks WHERE block_id=?`,
		record.BlockID).Scan(&canonicalAfter); err != nil || canonicalAfter != canonicalBefore {
		t.Fatalf("rebuild modified canonical payload: %v", err)
	}
	var foreignCount int
	if err := writer.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_exact_terms
		WHERE workspace_id='beta'`).Scan(&foreignCount); err != nil || foreignCount != 1 {
		t.Fatalf("rebuild removed another workspace's index: count=%d err=%v", foreignCount, err)
	}
	var retainedDescriptor string
	if err := writer.db.QueryRowContext(ctx, `SELECT payload_json FROM index_nodes
		WHERE node_id='descriptor:existing'`).Scan(&retainedDescriptor); err != nil ||
		retainedDescriptor != `{"source":"python"}` {
		t.Fatalf("record-derived rebuild erased catalog descriptor: %q, %v", retainedDescriptor, err)
	}
	if _, err := writer.db.ExecContext(ctx, `CREATE TRIGGER fail_rebuild BEFORE INSERT ON memory_exact_terms
		BEGIN SELECT RAISE(ABORT, 'injected index failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.RebuildDerivedRecordIndexes(ctx); err == nil {
		t.Fatal("injected derived-index failure was ignored")
	}
	afterFailure, err := index.MemoryIndexSHA256(ctx)
	if err != nil || afterFailure != before {
		t.Fatalf("failed rebuild partially replaced derived indexes: %q, %v", afterFailure, err)
	}
}

func TestRebuildBlockDescriptorIndexesUsesCurrentProfileScope(t *testing.T) {
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "UM_MEM_index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path, "alpha", "team", "mempalace")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx := context.Background()
	if _, err := writer.PrepareRootWrite(ctx, record); err != nil {
		t.Fatal(err)
	}
	catalog := memory.SemanticCatalog{RevisionID: "catalog:v1", Descriptors: []memory.BlockDescriptor{{
		BlockID: "leaf", BlockType: "semantic", Scope: "workspace", Authority: "verified",
		DeliveryPolicy: "on_demand", Retention: "durable", Producer: "agent",
		RecallPath: "hybrid_recall", SemanticMemberships: []string{"work", "leaf"},
		Guidance: memory.BlockGuidanceCard{BlockID: "leaf", Synopsis: "Work memory", UseWhen: []string{"work"}},
	}}}
	first, err := writer.RebuildBlockDescriptorIndexes(ctx, catalog)
	if err != nil {
		t.Fatal(err)
	}
	second, err := writer.RebuildBlockDescriptorIndexes(ctx, catalog)
	if err != nil || second != first {
		t.Fatalf("descriptor rebuild is not idempotent: %s, %s, %v", first, second, err)
	}
	var raw string
	if err := writer.db.QueryRowContext(ctx, `SELECT payload_json FROM index_nodes
		WHERE node_id='block_descriptor:alpha:leaf'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var node memory.IndexNode
	if err := json.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	if node.NodeType != "block_descriptor" || node.WorkspaceID != "alpha" ||
		node.Metadata["index_revision"] != float64(1) ||
		node.Metadata["last_update_reason"] != "rebuild" {
		t.Fatalf("incorrect descriptor metadata: %+v", node)
	}
	coverage, ok := node.Metadata["coverage"].(map[string]any)
	if !ok || coverage["canonical_record_count"] != float64(1) {
		t.Fatalf("incorrect descriptor coverage: %+v", node.Metadata["coverage"])
	}
	if _, err := writer.RebuildBlockDescriptorIndexes(ctx, memory.SemanticCatalog{
		RevisionID: "catalog:v2", Descriptors: []memory.BlockDescriptor{{BlockID: "other"}},
	}); err == nil {
		t.Fatal("missing logical descriptor was accepted")
	}
	var retained string
	if err := writer.db.QueryRowContext(ctx, `SELECT payload_json FROM index_nodes
		WHERE node_id='block_descriptor:alpha:leaf'`).Scan(&retained); err != nil || retained != raw {
		t.Fatalf("failed descriptor rebuild did not roll back: %v", err)
	}
}

func TestRebuildDerivedRecordIndexesReadsPythonV16Store(t *testing.T) {
	fixture, err := os.ReadFile("testdata/python_v16.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "python_v16.sqlite")
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	index, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	before, err := index.MemoryIndexSHA256(ctx)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path, "alpha", "team", "hindsight")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	rebuilt, err := writer.RebuildDerivedRecordIndexes(ctx)
	if err != nil || rebuilt.RecordCount != 1 {
		t.Fatalf("Python v16 index rebuild failed: %+v, %v", rebuilt, err)
	}
	if rebuilt.IndexSHA256 != before {
		t.Fatalf("Python v16 derived index changed after rebuild: before=%s after=%s", before, rebuilt.IndexSHA256)
	}
}
