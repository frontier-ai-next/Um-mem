package engine

import (
	"context"
	"testing"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestRegisterSourceAndExactByteCoverage(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	lineage := HostLineage{SessionID: "session-1", TaskID: "task-1", ProjectID: "project-1"}
	source, err := selected.RegisterSource(ctx, "source-1", "AéB", "host:test", "user", "", lineage)
	if err != nil || source["source_bytes"] != 4 || source["session_id"] != "session-1" {
		t.Fatalf("source registration lost exact bytes: %+v, %v", source, err)
	}
	replayed, err := selected.RegisterSource(ctx, "source-1", "AéB", "host:test", "user", "", lineage)
	if err != nil || replayed["witness_id"] != source["witness_id"] {
		t.Fatalf("source registration did not replay: %+v, %v", replayed, err)
	}
	coverage, err := selected.SourceCoverage(ctx, []string{"source-1"})
	if err != nil || coverage["source_bytes"] != 4 || coverage["covered_bytes"] != 0 {
		t.Fatalf("unprojected source coverage is wrong: %+v, %v", coverage, err)
	}
	segments := []any{
		map[string]any{"source_id": "source-1", "source_sha256": source["source_sha256"],
			"witness_id": source["witness_id"], "byte_start": 0, "byte_end": 1, "text": "A"},
		map[string]any{"source_id": "source-1", "source_sha256": source["source_sha256"],
			"witness_id": source["witness_id"], "byte_start": 1, "byte_end": 3, "text": "é"},
		map[string]any{"source_id": "source-1", "source_sha256": source["source_sha256"],
			"witness_id": source["witness_id"], "byte_start": 3, "byte_end": 4, "text": "B"},
	}
	if _, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "AéB",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1", RunID: "session-1",
		Metadata: map[string]any{"memory_mode": "lossless_atomic", "source_segments": segments}}); err == nil {
		t.Fatal("standard candidate path accepted forged lossless source proof")
	}
	record, err := memory.NewMemoryRecord("AéB", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	record.ProfileID, record.Kind, record.SessionID, record.RunID = "team", "fact", "session-1", "session-1"
	record.ProjectID = "project-1"
	record.Metadata = map[string]any{"memory_mode": "lossless_atomic", "source_segments": segments}
	record.RecordedAt = time.Now().UTC().Format(time.RFC3339Nano)
	record.DuplicateClusterID, err = memory.DuplicateClusterID(record)
	if err != nil {
		t.Fatal(err)
	}
	record.CanonicalKey, err = memory.DeriveCanonicalKey("alpha", "team", "fact", record.BlockID)
	if err != nil {
		t.Fatal(err)
	}
	record.BlockMembership = &memory.RecordBlockMembership{PrimaryBlockID: "stable_project_facts",
		SecondaryBlockIDs: []string{}}
	record.RevisionID, err = memory.RevisionIDForRecord(record.CanonicalKey, record, "insert", "")
	if err != nil {
		t.Fatal(err)
	}
	record.BlockID = memory.BlockIDForCanonicalKey(record.CanonicalKey)
	record.BlockMembership = nil
	if err := selected.BindRootRecord(ctx, &record, "stable_project_facts"); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.PutRoot(ctx, record); err != nil {
		t.Fatalf("trusted lossless fixture failed: %v", err)
	}
	changed := "changed"
	if _, err := selected.Amend(ctx, AmendRequest{MemoryID: record.BlockID,
		ExpectedRevision: record.RevisionID, Reason: "rewrite", Content: &changed}); err == nil {
		t.Fatal("generic amendment changed exact lossless source content")
	}
	coverage, err = selected.SourceCoverage(ctx, []string{"source-1"})
	if err != nil || coverage["source_bytes"] != 4 || coverage["covered_bytes"] != 4 ||
		coverage["source_byte_fraction"] != float64(1) ||
		coverage["source_id_fraction"] != float64(1) ||
		coverage["record_ids"].([]string)[0] != record.BlockID {
		t.Fatalf("exact UTF-8 source coverage failed: %+v, %v", coverage, err)
	}
	unknown, err := selected.SourceCoverage(ctx, []string{"source-1", "unknown"})
	if err != nil || unknown["source_bytes"] != nil || unknown["covered_bytes"] != nil ||
		len(unknown["unknown_source_ids"].([]string)) != 1 {
		t.Fatalf("unknown source falsely reported complete coverage: %+v, %v", unknown, err)
	}
}
