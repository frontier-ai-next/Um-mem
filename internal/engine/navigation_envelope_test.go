package engine

import (
	"context"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
)

func TestNavigationEnvelopeShowsPagedRelationsAndMaintenanceFacts(t *testing.T) {
	ctx := context.Background()
	selected, err := Open(Config{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	rootID := ""
	for _, block := range selected.Blocks() {
		if block.BlockType == "workspace_profile_root" {
			rootID = block.BlockID
		}
	}
	first, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Observed category",
		Synopsis: "Observed category", ParentID: rootID, Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := selected.CreateBlock(ctx, CreateBlockRequest{Name: "Related category",
		Synopsis: "Related category", ParentID: rootID, Reason: "test"})
	if err != nil {
		t.Fatal(err)
	}
	firstID, secondID := first["block_id"].(string), second["block_id"].(string)
	if _, err := selected.AddBlockEdge(ctx, firstID, secondID, "related_to", "related project work"); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.ObserveMaintenance(ctx, catalog.AddObservationRequest{BlockID: firstID,
		ObservationType: "capacity_pressure", Values: map[string]any{"utilization": 0.9},
		ThresholdRevision: "threshold-1", EvidenceIDs: []string{"run-1", "run-1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := selected.ObserveMaintenance(ctx, catalog.AddObservationRequest{BlockID: firstID,
		ObservationType: "low_usage", Values: map[string]any{"count": true},
		ThresholdRevision: "threshold-1"}); err == nil {
		t.Fatal("non-numeric maintenance fact was accepted")
	}
	envelope, err := selected.NavigationEnvelope(ctx, firstID, 1, "", "")
	if err != nil || envelope["ok"] != true || envelope["related_count"] != 1 ||
		len(envelope["related_blocks"].([]map[string]any)) != 1 ||
		envelope["related_blocks"].([]map[string]any)[0]["block_id"] != secondID ||
		len(envelope["maintenance_observations"].([]catalog.MaintenanceObservation)) != 1 ||
		len(envelope["proposal_options"].([]string)) != 2 ||
		envelope["mutation_performed"] != false {
		t.Fatalf("navigation envelope differs from catalog facts: %+v, %v", envelope, err)
	}
}
