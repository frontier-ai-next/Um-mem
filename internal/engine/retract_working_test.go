package engine

import (
	"context"
	"testing"
)

func TestRetractWorkingRecordRequiresOwnerAndCurrentCandidate(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "Unfinished observation",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1",
		Metadata: map[string]any{"working_set_owner": map[string]any{
			"owner_id": "owner-one", "generation_id": "generation-one"}}})
	if err != nil || !created.Saved {
		t.Fatalf("working candidate setup failed: %+v, %v", created, err)
	}
	old, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || old == nil {
		t.Fatal(err)
	}
	changedContent := "Corrected unfinished observation"
	changed, err := selected.Amend(ctx, AmendRequest{MemoryID: old.BlockID,
		ExpectedRevision: old.RevisionID, Reason: "same owner correction",
		Content: &changedContent, RequestedBlockID: "stable_project_facts"})
	if err != nil || changed["saved"] != true {
		t.Fatalf("working correction failed: %+v, %v", changed, err)
	}
	request := RetractWorkingRequest{MemoryID: old.BlockID, OwnerID: "owner-one",
		GenerationID: "generation-one", Reason: "stop work"}
	wrong := request
	wrong.GenerationID = "generation-two"
	if _, err := selected.RetractWorkingRecord(ctx, wrong); err == nil {
		t.Fatal("another owner generation retracted working candidate")
	}
	result, err := selected.RetractWorkingRecord(ctx, request)
	if err != nil || result["outcome"] != "retracted" || result["source_record_id"] != old.BlockID {
		t.Fatalf("owned working retraction failed: %+v, %v", result, err)
	}
	current, err := selected.ReadStored(ctx, result["record_id"].(string))
	if err != nil || current == nil || current.Status != "retracted" ||
		current.RevisionOf != changed["new_id"] {
		t.Fatalf("retraction did not follow current working revision: %+v, %v", current, err)
	}
	repeat, err := selected.RetractWorkingRecord(ctx, request)
	if err != nil || repeat["outcome"] != "already_retracted" ||
		repeat["record_id"] != current.BlockID {
		t.Fatalf("working retraction was not idempotent: %+v, %v", repeat, err)
	}
}
