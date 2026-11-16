package memory

import (
	"strings"
	"testing"
	"time"
)

func TestProcedureEntryReportsFreshnessAndChecklist(t *testing.T) {
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	record := MemoryRecord{BlockID: "mem_procedure", Title: "Restart the local service",
		Content: "Restart the MCP service after editing the source.", Kind: "procedure",
		Tags: []string{"um-mem"}, RecordedAt: "2026-10-01T00:00:00Z",
		Metadata: map[string]any{"steps": []any{"edit the source", "restart the service"},
			"verify": "the health call answers", "verified_at": "2026-09-01T00:00:00Z",
			"stale_after_days": 30}}
	entry := ProcedureEntry(record, nil, now)
	if entry["freshness"] != "stale" || entry["age_days"] != 53 ||
		entry["verify"] != "the health call answers" || len(entry["steps"].([]string)) != 2 {
		t.Fatalf("procedure checklist lost freshness or steps: %+v", entry)
	}
	record.Metadata = map[string]any{}
	if got := ProcedureEntry(record, nil, now)["freshness"]; got != "unverified" {
		t.Fatalf("unconfirmed procedure was labeled %v", got)
	}
}

func TestProcedureFreshnessTracksEnvironmentChanges(t *testing.T) {
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	descriptor := map[string]any{"um_mem_version": "0.6.0", "semantic_model": "model-a",
		"store": "/tmp/store", "workspace": "ws"}
	metadata := map[string]any{"verified_at": "2026-10-21T00:00:00Z",
		"stale_after_days": 365, "fingerprint": descriptor}
	if got := EvaluateProcedureFreshness(metadata, descriptor, now); got.Freshness != "fresh" || got.AgeDays != 3 {
		t.Fatalf("unchanged procedure environment was not fresh: %+v", got)
	}
	moved := map[string]any{"um_mem_version": "0.6.0", "semantic_model": "model-b",
		"store": "/tmp/store", "workspace": "ws"}
	if got := EvaluateProcedureFreshness(metadata, moved, now); got.Freshness != "stale" ||
		!strings.Contains(got.Reason, "semantic_model") {
		t.Fatalf("changed model did not stale the procedure: %+v", got)
	}
}
