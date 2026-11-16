package engine

import (
	"context"
	"testing"
)

func TestDeprecateArchivesCanonicalHeadAndRetainsHistory(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := selected.RememberCandidate(ctx, CandidateRequest{Content: "Outdated project fact",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1"})
	if err != nil || !created.Saved {
		t.Fatalf("candidate setup failed: %+v, %v", created, err)
	}
	old, err := selected.ReadStored(ctx, created.RecordID)
	if err != nil || old == nil {
		t.Fatal(err)
	}
	if _, err := selected.Deprecate(ctx, DeprecateRequest{MemoryID: old.BlockID}); err == nil {
		t.Fatal("deprecation without a reason was accepted")
	}
	request := DeprecateRequest{MemoryID: old.BlockID, Reason: "superseded by new evidence",
		ExpectedRevision: old.RevisionID}
	deprecated, err := selected.Deprecate(ctx, request)
	if err != nil || deprecated["saved"] != true || deprecated["revision_of"] != old.BlockID {
		t.Fatalf("deprecation failed: %+v, %v", deprecated, err)
	}
	id := deprecated["block_id"].(string)
	head, err := selected.ReadStored(ctx, id)
	if err != nil || head == nil || head.Status != "deprecated" ||
		head.Retention != "archive" || head.DeliveryPolicy != "hidden" ||
		head.Metadata["status_reason"] != request.Reason || head.RevisionOf != old.BlockID {
		t.Fatalf("deprecation did not archive an immutable successor: %+v, %v", head, err)
	}
	prior, err := selected.ReadStored(ctx, old.BlockID)
	if err != nil || prior == nil || prior.Content != old.Content || prior.Status != "candidate" {
		t.Fatalf("deprecation changed history: %+v, %v", prior, err)
	}
	replay, err := selected.Deprecate(ctx, request)
	if err != nil || replay["already_deprecated"] != true || replay["block_id"] != id {
		t.Fatalf("deprecation retry changed the head: %+v, %v", replay, err)
	}
	currentRetry, err := selected.Deprecate(ctx, DeprecateRequest{MemoryID: id, Reason: request.Reason})
	if err != nil || currentRetry["already_deprecated"] != true || currentRetry["block_id"] != id {
		t.Fatalf("current-head deprecation retry changed the head: %+v, %v", currentRetry, err)
	}
	stale, err := selected.Deprecate(ctx, DeprecateRequest{MemoryID: old.BlockID,
		Reason: "wrong revision", ExpectedRevision: "stale"})
	if err != nil || stale["saved"] != false || stale["error"] != "expected_revision_conflict" {
		t.Fatalf("stale deprecation bypassed expected revision: %+v, %v", stale, err)
	}
}
