package catalog

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMaintenanceObservationsStayInProfileAndValidateFacts(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	opened, err := Open(repo, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	states, err := opened.ListBlocks(ctx, false)
	if err != nil || len(states) != 1 {
		t.Fatalf("catalog root missing: %+v, %v", states, err)
	}
	rootID := states[0].Block.BlockID
	if _, err := opened.AddObservation(ctx, AddObservationRequest{BlockID: rootID,
		ObservationType: "capacity_pressure", Values: map[string]any{"used": true},
		ThresholdRevision: "threshold-1"}); err == nil {
		t.Fatal("Boolean maintenance fact was accepted")
	}
	item, err := opened.AddObservation(ctx, AddObservationRequest{BlockID: rootID,
		ObservationType: "capacity_pressure", Values: map[string]any{"used": json.Number("0.75")},
		ThresholdRevision: "threshold-1", EvidenceIDs: []string{"run-1", "run-1", "run-2"}})
	if err != nil || item.ObservationID == "" || len(item.EvidenceIDs) != 2 {
		t.Fatalf("observation write differs: %+v, %v", item, err)
	}
	read, err := opened.Observations(ctx, rootID)
	if err != nil || len(read) != 1 || read[0].ObservationID != item.ObservationID ||
		read[0].Values["used"] != json.Number("0.75") {
		t.Fatalf("observation read differs: %+v, %v", read, err)
	}
	other, err := Open(repo, "alpha", "other")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	foreign, err := other.Observations(ctx, rootID)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("observation leaked across profiles: %+v, %v", foreign, err)
	}
}
