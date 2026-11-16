package engine

import (
	"context"
	"fmt"
	"testing"
)

func TestProcedureDiscoverySurvivesNewerObservationWindow(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	selected.projector = verifiedProjector{}
	created, err := selected.RememberCandidate(ctx, CandidateRequest{
		Content: "Restart the local service after configuration changes",
		Title:   "Restart local service", Kind: "procedure",
		RequestedBlockID: "stable_project_facts", EvidenceKind: "agent_tool_claim",
		Metadata: map[string]any{"steps": []any{"stop", "start"}},
	})
	if err != nil || !created.Saved {
		t.Fatalf("procedure setup failed: %+v, %v", created, err)
	}
	for index := range 129 {
		observed, err := selected.RememberCandidate(ctx, CandidateRequest{
			Content:          fmt.Sprintf("Unrelated observation number %03d", index),
			RequestedBlockID: "stable_project_facts",
		})
		if err != nil || !observed.Saved {
			t.Fatalf("newer observation %d failed: %+v, %v", index, observed, err)
		}
	}
	recent, err := selected.Recent(ctx, RecentRequest{Limit: 128, Scope: "workspace",
		ScopeID: "alpha", IncludeCandidates: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range recent {
		if record.BlockID == created.RecordID {
			t.Fatal("test procedure unexpectedly remained inside recent fallback window")
		}
	}
	procedures, err := selected.Procedures(ctx, ProcedureRequest{Query: "restart local service", Limit: 3})
	if err != nil || len(procedures) != 1 || procedures[0]["record_id"] != created.RecordID {
		t.Fatalf("older current procedure was not discoverable: %+v, %v", procedures, err)
	}
}
