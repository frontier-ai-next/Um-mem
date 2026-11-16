package telemetry

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestProfileSpansRecordNestedTimingWithoutContent(t *testing.T) {
	repo := t.TempDir()
	ctx, parent := StartProfile(context.Background(), repo, ProfileOptions{
		Operation: "build_context", Component: "memory_client", WorkspaceID: "alpha",
		ProfileID: "team", UserID: "user-a", ChatID: "chat-a", RunID: "run-a",
		Backend: "mnemosyne", Input: map[string]any{"query": "do not persist this query"}})
	_, child := StartProfile(ctx, repo, ProfileOptions{Operation: "backend_recall",
		Component: "mnemosyne", WorkspaceID: "alpha", Backend: "mnemosyne",
		Input: map[string]any{"query": "do not persist this query", "limit": 8}})
	child.SetOutput(map[string]any{"hit_count": 3})
	child.SetResult("ok", "", "returned", "native_recall")
	child.End(nil)
	parent.SetOutput(map[string]any{"text": "also do not persist this result"})
	parent.End(nil)
	data, err := os.ReadFile(Path(repo))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "do not persist") {
		t.Fatal("profile event disclosed input or output content")
	}
	rows, invalid, err := readEventRows(repo, 10)
	if err != nil || invalid != 0 || len(rows) != 2 {
		t.Fatalf("profile rows are invalid: %d rows, %d invalid, %v", len(rows), invalid, err)
	}
	if rows[0]["parent_span_id"] == "" || rows[0]["user_id"] != "user-a" ||
		rows[0]["chat_id"] != "chat-a" {
		t.Fatalf("nested profile lost identity: %+v", rows[0])
	}
	parallel := rows[0]["parallelism"].(map[string]any)
	if number(parallel["max_in_flight"]) != 2 {
		t.Fatalf("nested overlap was not measured: %+v", parallel)
	}
	report, err := ProfileReport(repo, 10)
	if err != nil || report["profiled_operation_count"] != 2 || report["max_in_flight"] != 2 {
		t.Fatalf("profile report lost nested spans: %+v %v", report, err)
	}
	operations := report["by_operation"].(map[string]any)
	if operations["backend_recall"].(map[string]any)["count"] != 1 ||
		operations["build_context"].(map[string]any)["count"] != 1 {
		t.Fatalf("profile operation counts are wrong: %+v", operations)
	}
}

func TestProfileReportIsEmptyBeforeAnyTraceExists(t *testing.T) {
	report, err := ProfileReport(t.TempDir(), 100)
	if err != nil || report["profiled_operation_count"] != 0 || report["invalid_event_count"] != 0 {
		t.Fatalf("fresh profile report must be empty: %+v %v", report, err)
	}
}
