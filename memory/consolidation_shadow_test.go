package memory

import (
	"strings"
	"testing"
)

func TestConsolidationShadowCommitsOnlyAcceptedCandidate(t *testing.T) {
	transition, report := tribunalFixturePair(t, nil, "safe base", []string{"episode-rare"}, nil, nil)
	store := NewConsolidationShadowStore()
	snapshot, err := store.Stage("ws", transition)
	if err != nil {
		t.Fatal(err)
	}
	originalViewPayload := snapshot.ViewPayloads[0].Payload
	snapshot.ViewPayloads[0].Payload = "forged view"
	if _, err := store.Stage("ws", transition); err == nil || !strings.Contains(err.Error(), "one bounded candidate") {
		t.Fatalf("shadow allowed a second pending candidate: %v", err)
	}
	forged := report
	forged.Assessment.PostStateCandidateSHA256 = "sha256:" + strings.Repeat("0", 64)
	if _, err := store.Resolve(snapshot.SnapshotID, forged, true); err == nil {
		t.Fatal("shadow accepted a mismatched tribunal report")
	}
	if store.ActiveHead("ws") != "" {
		t.Fatal("unresolved candidate changed active head")
	}
	resolution, err := store.Resolve(snapshot.SnapshotID, report, true)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Disposition != "committed" || resolution.CommitReceipt == nil ||
		resolution.RejectionReceipt != nil || resolution.CommitReceipt.PreviousHeadSHA256 != "" ||
		store.ActiveHead("ws") != transition.PostStateCandidateSHA256 ||
		len(store.ActiveViews("ws")) != 1 ||
		store.ActiveViews("ws")["view-release"] != originalViewPayload {
		t.Fatalf("accepted candidate did not atomically become active: %+v", resolution)
	}
	views := store.ActiveViews("ws")
	delete(views, "view-release")
	if len(store.ActiveViews("ws")) != 1 {
		t.Fatal("active view caller mutated shadow state")
	}
	if _, err := store.Resolve(snapshot.SnapshotID, report, true); err == nil {
		t.Fatal("shadow resolved the same snapshot twice")
	}
}

func TestConsolidationShadowQuarantinesRejectedRevisionWithoutChangingHead(t *testing.T) {
	store := NewConsolidationShadowStore()
	baseTransition, baseReport := tribunalFixturePair(t, nil, "safe base", []string{"episode-rare"}, nil, nil)
	baseSnapshot, err := store.Stage("ws", baseTransition)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(baseSnapshot.SnapshotID, baseReport, true); err != nil {
		t.Fatal(err)
	}
	previousHead := store.ActiveHead("ws")
	previousViews := store.ActiveViews("ws")
	rejectedTransition, rejectedReport := tribunalFixturePair(t, nil, "unsafe rewrite", []string{"episode-rare"}, nil,
		func(semantic *TransitionSemanticVerification) { semantic.Faithfulness = false })
	rejectedSnapshot, err := store.Stage("ws", rejectedTransition)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := store.Resolve(rejectedSnapshot.SnapshotID, rejectedReport, true)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Disposition != "quarantined" || resolution.RejectionReceipt == nil ||
		resolution.CommitReceipt != nil || len(resolution.RejectionReceipt.VerifierReasons) == 0 ||
		store.ActiveHead("ws") != previousHead ||
		store.ActiveViews("ws")["view-release"] != previousViews["view-release"] {
		t.Fatalf("rejected candidate changed the active view: %+v", resolution)
	}
	if _, err := store.Stage("ws", rejectedTransition); err == nil ||
		!strings.Contains(err.Error(), "previously rejected") {
		t.Fatalf("rejected candidate revision was staged again: %v", err)
	}
}

func TestConsolidationInputGateKeepsUnqualifiedStreamsInShadow(t *testing.T) {
	for _, mode := range []string{"heterogeneous", "streamed"} {
		result, err := EvaluateConsolidationInput(mode, false)
		if err != nil || result.ConsolidationAllowed || !result.ShadowOnly ||
			result.FallbackAction != "retain" {
			t.Fatalf("unqualified %s input escaped shadow gate: %+v, %v", mode, result, err)
		}
	}
	qualified, err := EvaluateConsolidationInput("streamed", true)
	if err != nil || !qualified.ConsolidationAllowed || qualified.ShadowOnly ||
		qualified.FallbackAction != "defer" {
		t.Fatalf("qualified stream did not pass input gate: %+v, %v", qualified, err)
	}
	if _, err := EvaluateConsolidationInput("unknown", true); err == nil {
		t.Fatal("unknown input mode passed consolidation gate")
	}
}
