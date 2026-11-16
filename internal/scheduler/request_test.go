package scheduler

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEventFromRequestBindsIdentityAndRejectsHostContent(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	request := map[string]any{"event_id": "codex-host-state:abc", "event_type": "prospective_trigger",
		"run_id": "codex-session:abc", "stage_id": "codex-turn:def",
		"signal_codes":     []any{"schema:1", "codex_event:UserPromptSubmit"},
		"capability_codes": []any{"typed_host_state", "codex_hook"},
		"retry_count":      json.Number("0")}
	event, err := EventFromRequest(request, "alpha", "policy-9", now)
	if err != nil || event.WorkspaceID != "alpha" || event.StateRevision != "policy-9" ||
		!event.OccurredAt.Equal(now) {
		t.Fatalf("host identity was not bound: %+v, %v", event, err)
	}
	request["prompt"] = "private user text"
	if _, err := EventFromRequest(request, "alpha", "policy-9", now); err == nil {
		t.Fatal("host content entered a scheduled event")
	}
	delete(request, "prompt")
	request["occurred_at"] = "2026-10-24T12:00:00"
	if _, err := EventFromRequest(request, "alpha", "policy-9", now); err == nil {
		t.Fatal("naive event timestamp accepted")
	}
	request["occurred_at"] = "2026-10-24T12:00:00.123456+00:00"
	event, err = EventFromRequest(request, "alpha", "policy-9", now)
	if err != nil || event.OccurredAt.Nanosecond() != 123456000 {
		t.Fatalf("aware event timestamp rejected: %+v %v", event, err)
	}
	request["retry_count"] = true
	if _, err := EventFromRequest(request, "alpha", "policy-9", now); err == nil {
		t.Fatal("boolean retry count accepted")
	}
}
