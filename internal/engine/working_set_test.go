package engine

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestWorkingSetKeepsCandidatePrivateToExactOwnerAndBudget(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", BackendID: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
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
	record.Content = strings.Repeat("Current unfinished experiment ", 100)
	record.Metadata = map[string]any{"working_set_owner": map[string]any{
		"owner_id": "node-a", "generation_id": "generation-1"},
		"private_transport": "must-not-project"}
	record.ContentMode = "full"
	if err := selected.BindRootRecord(ctx, &record, "stable_project_facts"); err != nil {
		t.Fatal(err)
	}
	selected.projector = verifiedProjector{}
	if _, err := selected.PutRoot(ctx, record); err != nil {
		t.Fatal(err)
	}
	ids := []string{record.BlockID, "missing"}
	working, err := selected.ReadWorkingSet(ctx, "node-a", "generation-1", ids, 256, 512,
		[]string{"workspace:alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(working.Records) != 1 || working.Records[0].RecordID != record.BlockID ||
		working.Records[0].LogicalBlockID != "stable_project_facts" ||
		len(working.UnavailableRecordIDs) != 1 || working.UnavailableRecordIDs[0] != "missing" ||
		working.RenderedBytes > 512 || working.EstimatedTokens > 256 ||
		!working.Truncated || !strings.Contains(working.RenderedContext, "Current unfinished experiment") ||
		strings.Contains(working.RenderedContext, "private_transport") {
		t.Fatalf("owner-scoped working read changed content or budget: %+v", working)
	}
	foreign, err := selected.ReadWorkingSet(ctx, "node-b", "generation-1", ids, 256, 512,
		[]string{"workspace:alpha"})
	if err != nil || len(foreign.Records) != 0 || foreign.RenderedContext != "" ||
		len(foreign.UnavailableRecordIDs) != 2 {
		t.Fatalf("foreign owner accessed candidate: %+v, %v", foreign, err)
	}
	tiny, err := selected.ReadWorkingSet(ctx, "node-a", "generation-1", []string{record.BlockID}, 1, 1,
		[]string{"workspace:alpha"})
	if err != nil || tiny.RenderedContext != "" || tiny.RenderedBytes != 0 ||
		len(tiny.OmittedRecordIDs) != 1 || tiny.OmittedRecordIDs[0] != record.BlockID {
		t.Fatalf("tiny working budget leaked candidate content: %+v, %v", tiny, err)
	}
	wrongScope, err := selected.ReadWorkingSet(ctx, "node-a", "generation-1", []string{record.BlockID}, 256, 512,
		[]string{"session:another"})
	if err != nil || len(wrongScope.Records) != 0 || len(wrongScope.UnavailableRecordIDs) != 1 {
		t.Fatalf("candidate crossed explicit scope: %+v, %v", wrongScope, err)
	}
	pinned, err := selected.PinRecord(ctx, record.BlockID, "node-a", "generation-1",
		[]string{"workspace:alpha"})
	if err != nil || pinned.Status != "candidate" || pinned.ContentHash == "" ||
		pinned.LogicalBlockID != "stable_project_facts" {
		t.Fatalf("owner could not pin exact candidate: %+v, %v", pinned, err)
	}
	if _, err := selected.PinRecord(ctx, record.BlockID, "node-b", "generation-1",
		[]string{"workspace:alpha"}); err == nil {
		t.Fatal("foreign owner pinned candidate")
	}
	snapshot, err := selected.SnapshotWorkingRecord(ctx, record.BlockID, "node-a",
		"generation-1", pinned.RevisionID, pinned.ContentHash, 10000, 100000,
		[]string{"workspace:alpha"})
	if err != nil || snapshot.Content != record.Content || snapshot.Reference != pinned {
		t.Fatalf("exact working snapshot changed candidate: %+v, %v", snapshot, err)
	}
	if _, err := selected.SnapshotWorkingRecord(ctx, record.BlockID, "node-a",
		"generation-1", pinned.RevisionID, strings.Repeat("0", 64), 10000, 100000,
		[]string{"workspace:alpha"}); err == nil {
		t.Fatal("working snapshot accepted a wrong content digest")
	}
	if _, err := selected.SnapshotWorkingRecord(ctx, record.BlockID, "node-a",
		"generation-1", pinned.RevisionID, pinned.ContentHash, 1, 100,
		[]string{"workspace:alpha"}); err == nil {
		t.Fatal("working snapshot silently truncated complete candidate")
	}
}
