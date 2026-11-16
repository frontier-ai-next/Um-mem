package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/frontier-ai-next/Um-mem/internal/ledger"
	"github.com/frontier-ai-next/Um-mem/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPPromotionBindsHostLineageAndPublishes(t *testing.T) {
	t.Setenv("UM_MEM_SESSION_ID", "trusted-session")
	t.Setenv("UM_MEM_PROJECT_ID", "trusted-project")
	t.Setenv("UM_MEM_TASK_ID", "")
	t.Setenv("UM_MEM_SUBTASK_ID", "")
	t.Setenv("UM_MEM_USER_ID", "")
	ctx := context.Background()
	root := t.TempDir()
	selected, err := engine.Open(engine.Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, engine.CandidateRequest{Content: "MCP reviewed claim",
		RequestedBlockID: "stable_project_facts", SessionID: "trusted-session"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	source, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || source == nil {
		t.Fatal(err)
	}
	server := New(selected, "alpha", "mnemosyne")
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "go-test", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	forged, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "verification_subject",
		Arguments: map[string]any{"memory_id": source.BlockID,
			"expected_source_revision_id": source.RevisionID,
			"lineage":                     map[string]any{"project_id": "other-project"}}})
	if err != nil || forged.StructuredContent.(map[string]any)["ok"] != false {
		t.Fatalf("MCP accepted a forged project lineage: %+v, %v", forged, err)
	}
	subjectResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "verification_subject",
		Arguments: map[string]any{"memory_id": source.BlockID,
			"expected_source_revision_id": source.RevisionID}})
	if err != nil || subjectResult.IsError {
		diagnostic, _ := json.Marshal(subjectResult)
		t.Fatalf("MCP verification subject failed: %s, %v", diagnostic, err)
	}
	encoded, err := json.Marshal(subjectResult.StructuredContent.(map[string]any)["subject"])
	if err != nil {
		t.Fatal(err)
	}
	var subject memory.CandidateVerificationSubject
	if err := json.Unmarshal(encoded, &subject); err != nil {
		t.Fatal(err)
	}
	report, err := ledger.AppendCandidateSemanticVerification(ledger.CandidateSemanticVerification{
		RepoRoot: root, Subject: subject, VerifierID: "reviewer-1",
		VerifierModelVersion: "review-model", ProposerModelVersion: "author-model",
		Coverage: true, Preservation: true, Faithfulness: true, InstructionSafety: true,
		Reasons: []string{"MCP candidate independently reviewed"}})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"memory_id": source.BlockID,
		"expected_source_revision_id": source.RevisionID,
		"reason":                      "review passed", "operation_id": "mcp-promotion-1",
		"evidence_refs": []any{report}}
	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "promote", Arguments: args})
	if err != nil || result.IsError || result.StructuredContent.(map[string]any)["saved"] != true {
		t.Fatalf("MCP promotion failed: %+v, %v", result, err)
	}
	acceptedID := result.StructuredContent.(map[string]any)["block_id"].(string)
	replay, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "promote", Arguments: args})
	if err != nil || replay.IsError || replay.StructuredContent.(map[string]any)["replayed"] != true ||
		replay.StructuredContent.(map[string]any)["block_id"] != acceptedID {
		t.Fatalf("MCP promotion replay failed: %+v, %v", replay, err)
	}
}
