package engine

import (
	"context"
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func TestAddEdgeRequiresEvidenceAndRetainedScopedRecords(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	ids := make([]string, 0, 2)
	for _, content := range []string{"Source finding", "Dependent finding"} {
		result, err := selected.RememberCandidate(ctx, CandidateRequest{Content: content,
			RequestedBlockID: "stable_project_facts", SessionID: "session-1", RunID: "session-1"})
		if err != nil || !result.Saved {
			t.Fatalf("candidate setup failed: %+v, %v", result, err)
		}
		ids = append(ids, result.RecordID)
	}
	request := AddEdgeRequest{SrcID: ids[1], DstID: ids[0], EdgeType: "derived_from",
		Reason: "source link", EvidenceRefs: []memory.EvidenceRef{{RefType: "test_run",
			RefID: "edge-run", ProducedBy: "verifier", ProducerID: "go-test"}}}
	if _, err := selected.AddEdge(ctx, AddEdgeRequest{SrcID: ids[1], DstID: ids[0],
		EdgeType: "derived_from"}); err == nil {
		t.Fatal("public graph edge accepted no evidence")
	}
	if _, err := selected.AddEdge(ctx, AddEdgeRequest{SrcID: ids[1], DstID: "foreign-record",
		EdgeType: "derived_from", EvidenceRefs: request.EvidenceRefs}); err == nil {
		t.Fatal("public graph edge accepted an unknown endpoint")
	}
	if _, err := selected.AddEdge(ctx, AddEdgeRequest{SrcID: ids[1], DstID: ids[0],
		EdgeType: "supersedes", EvidenceRefs: request.EvidenceRefs}); err == nil {
		t.Fatal("public graph edge accepted writer-owned supersession")
	}
	if _, err := selected.AddEdge(ctx, AddEdgeRequest{SrcID: ids[1], DstID: ids[0],
		EdgeType: "derived_from", Reason: "agent_authored_from_raw_witness",
		EvidenceRefs: request.EvidenceRefs}); err == nil {
		t.Fatal("public graph edge impersonated writer-owned raw witness provenance")
	}
	first, err := selected.AddEdge(ctx, request)
	if err != nil || first["ok"] != true || first["replayed"] != false {
		t.Fatalf("evidenced graph edge failed: %+v, %v", first, err)
	}
	repeated, err := selected.AddEdge(ctx, request)
	if err != nil || repeated["replayed"] != true {
		t.Fatalf("evidenced graph edge was not idempotent: %+v, %v", repeated, err)
	}
	exported, err := selected.ExportGraph(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported["blocks"].([]memory.MemoryRecord)) != 2 ||
		len(exported["edges"].([]memory.EdgeRecord)) != 1 ||
		exported["audit"].(map[string]any)["edge_count"] != 1 {
		t.Fatalf("graph export lost scoped canonical records or edge: %+v", exported)
	}
	routes := exported["provenance_routes"].(map[string][]map[string]any)
	if len(routes[ids[1]]) != 1 || routes[ids[1]][0]["target_id"] != ids[0] {
		t.Fatalf("graph export lost provenance route: %+v", routes)
	}
}

func TestGraphCyclesFindsEvidenceChainLoop(t *testing.T) {
	cycles := graphCycles([]memory.EdgeRecord{{SrcID: "b", DstID: "a", EdgeType: "derived_from"},
		{SrcID: "a", DstID: "b", EdgeType: "revises"}})
	if len(cycles) != 1 || len(cycles[0]) != 3 || cycles[0][0] != cycles[0][2] {
		t.Fatalf("graph cycle audit missed loop: %+v", cycles)
	}
}

func TestExportGraphDoesNotExposeOtherProfile(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	for _, profile := range []string{"team-a", "team-b"} {
		selected, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha",
			ProfileID: profile, BackendID: "mnemosyne"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := selected.RememberCandidate(ctx, CandidateRequest{Content: profile + " private fact",
			RequestedBlockID: "stable_project_facts", SessionID: "session-1", RunID: "session-1"})
		if err != nil || !result.Saved {
			t.Fatalf("profile setup failed: %+v, %v", result, err)
		}
		if err := selected.Close(); err != nil {
			t.Fatal(err)
		}
	}
	selected, err := Open(Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team-a", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	exported, err := selected.ExportGraph(ctx)
	if err != nil {
		t.Fatal(err)
	}
	blocks := exported["blocks"].([]memory.MemoryRecord)
	if len(blocks) != 1 || blocks[0].ProfileID != "team-a" ||
		len(exported["edges"].([]memory.EdgeRecord)) != 0 {
		t.Fatalf("graph export crossed profile boundary: %+v", exported)
	}
}
