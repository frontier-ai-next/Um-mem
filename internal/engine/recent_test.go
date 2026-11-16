package engine

import (
	"context"
	"os"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestRecentKeepsOnlyCurrentCommittedRevision(t *testing.T) {
	fixture, err := os.ReadFile("../../memory/testdata/record_bound_v9.json")
	if err != nil {
		t.Fatal(err)
	}
	record, err := memory.DecodeCanonicalRecord(fixture)
	if err != nil {
		t.Fatal(err)
	}
	record.BlockMembership = nil
	record.SemanticCatalogVersion = ""
	record.CanonicalKey = "alpha:recent"
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	ctx := context.Background()
	selected.projector = verifiedProjector{}
	if err := selected.BindRootRecord(ctx, &record, "stable_project_facts"); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.PutRoot(ctx, record); err != nil {
		t.Fatal(err)
	}
	request := RecentRequest{Limit: 8, Scope: "workspace", IncludeCandidates: true}
	if rows, err := selected.Recent(ctx, request); err != nil || len(rows) != 1 || rows[0].BlockID != record.BlockID {
		t.Fatalf("committed current record absent from recent: %+v, %v", rows, err)
	}
	successor := record
	successor.BlockMembership = nil
	successor.SemanticCatalogVersion = ""
	successor.BlockID, successor.RevisionID = "mem_recent_corrected", "rev_recent_corrected"
	successor.RevisionOf = record.BlockID
	successor.SupersedesIDs = []string{record.BlockID}
	successor.Content = "Corrected recent fact"
	successor.ValidFrom = "2024-02-01T00:00:00Z"
	successor.RecordedAt, successor.UpdatedAt = successor.ValidFrom, successor.ValidFrom
	if err := selected.BindSuccessorRecord(ctx, &successor, "stable_project_facts"); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.PutSuccessor(ctx, successor, "correction", ""); err != nil {
		t.Fatal(err)
	}
	rows, err := selected.Recent(ctx, request)
	if err != nil || len(rows) != 1 || rows[0].BlockID != successor.BlockID {
		t.Fatalf("recent retained superseded version: %+v, %v", rows, err)
	}
	request.Since = "2024-03-01T00:00:00Z"
	if rows, err := selected.Recent(ctx, request); err != nil || len(rows) != 0 {
		t.Fatalf("recent ignored since cutoff: %+v, %v", rows, err)
	}
	request.Since = ""
	request.IncludeCandidates = false
	if rows, err := selected.Recent(ctx, request); err != nil || len(rows) != 0 {
		t.Fatalf("recent exposed candidate after opt-out: %+v, %v", rows, err)
	}
	request.IncludeCandidates = true
	request.ScopeID = "other"
	if rows, err := selected.Recent(ctx, request); err != nil || len(rows) != 0 {
		t.Fatalf("recent crossed explicit scope: %+v, %v", rows, err)
	}
}
