package localservice

import (
	"context"
	"strings"
	"testing"
)

func TestConsolidationServiceRejectsWrongRoleAndForgedLineage(t *testing.T) {
	args := map[string]any{"source_ids": []any{"source-a", "source-b"},
		"content": "bounded summary", "title": "Summary", "reason": "reviewed",
		"requested_block_id": "stable_project_facts", "session_id": "foreign-session"}
	caller := Caller{Role: "host_hook", Lineage: Lineage{SessionID: "session-1"}}
	if _, err := dispatchConsolidateObservations(context.Background(), nil, caller, args); err == nil || !strings.Contains(err.Error(), "supervisor or operator") {
		t.Fatalf("host hook reached supervisor consolidation: %v", err)
	}
	caller.Role = "supervisor"
	if _, err := dispatchConsolidateObservations(context.Background(), nil, caller, args); err == nil || !strings.Contains(err.Error(), "outside caller scope") {
		t.Fatalf("forged consolidation lineage was accepted: %v", err)
	}
	args["extra"] = true
	if _, err := dispatchConsolidateObservations(context.Background(), nil, caller, args); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown consolidation argument was accepted: %v", err)
	}
}
