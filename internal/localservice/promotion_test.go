package localservice

import (
	"context"
	"encoding/json"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/ledger"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestSupervisorServicePromotionBindsLeaseAndReplays(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := engine.Open(engine.Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, engine.CandidateRequest{Content: "Supervisor reviewed claim",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	source, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || source == nil {
		t.Fatal(err)
	}
	server, err := New(Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team",
		Backend: "mnemosyne"}, map[string]string{"supervisor": "supervisor-secret"},
		EngineDispatcher(selected))
	if err != nil {
		t.Fatal(err)
	}
	lease := frame()
	lease["caller_role"], lease["session_id"] = "supervisor", "session-1"
	lease["bootstrap_secret"] = "supervisor-secret"
	lease["scope_lineage"] = map[string]any{"workspace_id": "alpha",
		"session_id": "session-1", "project_id": "project-1"}
	lease["capabilities"] = server.capabilities["supervisor"]
	granted := post(t, server, "/lease", lease)
	token, ok := granted["lease"].(string)
	if granted["ok"] != true || !ok {
		t.Fatalf("supervisor lease failed: %+v", granted)
	}
	call := frame()
	call["caller_role"], call["session_id"], call["lease"] = "supervisor", "session-1", token
	call["tool"] = "verification_subject"
	call["arguments"] = map[string]any{"memory_id": source.BlockID,
		"expected_source_revision_id": source.RevisionID,
		"lineage":                     map[string]any{"project_id": "foreign-project"}}
	if forged := post(t, server, "/call", call); forged["ok"] != false {
		t.Fatalf("service accepted forged promotion lineage: %+v", forged)
	}
	call["arguments"] = map[string]any{"memory_id": source.BlockID,
		"expected_source_revision_id": source.RevisionID}
	verified := post(t, server, "/call", call)
	if verified["ok"] != true {
		t.Fatalf("service subject failed: %+v", verified)
	}
	encoded, err := json.Marshal(verified["subject"])
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
		Reasons: []string{"The supervisor checked the candidate"}})
	if err != nil {
		t.Fatal(err)
	}
	call["tool"] = "promote"
	call["arguments"] = map[string]any{"memory_id": source.BlockID,
		"expected_source_revision_id": source.RevisionID, "reason": "verified",
		"operation_id": "service-promotion-1", "evidence_refs": []any{report}}
	approved := post(t, server, "/call", call)
	if approved["ok"] != true || approved["saved"] != true {
		t.Fatalf("supervisor publication failed: %+v", approved)
	}
	replayed := post(t, server, "/call", call)
	if replayed["ok"] != true || replayed["replayed"] != true ||
		replayed["block_id"] != approved["block_id"] {
		t.Fatalf("supervisor publication replay failed: %+v", replayed)
	}
}
