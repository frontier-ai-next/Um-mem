package scheduler

import (
	"testing"
	"time"
)

func TestHostStateAndDefaultPlanMatchPython(t *testing.T) {
	event := HostStateEvent{EventID: "codex-host-state:abc", EventType: "prospective_trigger",
		WorkspaceID: "alpha", RunID: "codex-session:abc", StageID: "codex-turn:abc",
		StateRevision: "bundle:1", OccurredAt: time.Date(2026, 10, 24, 4, 0, 0, 0, time.UTC),
		SignalCodes:     []string{"schema:1", "codex_event:UserPromptSubmit", "schema:1"},
		CapabilityCodes: []string{"typed_host_state", "codex_hook"}}
	fingerprint, err := event.Fingerprint()
	if err != nil || fingerprint != "8ec734fa6b2805d716fc156c281d55f5ae364909bbefbcaccb7f9f9fbe17e4ba" {
		t.Fatalf("Python event fingerprint differs: %s, %v", fingerprint, err)
	}
	plan, err := DefaultPlan(event)
	if err != nil {
		t.Fatal(err)
	}
	record, err := plan.Record()
	if err != nil || record["plan_sha256"] != "a9378b30107a021492e20af719fcabd58045444f88f1423f804169119d8ab285" {
		t.Fatalf("Python default scheduler plan differs: %+v, %v", record, err)
	}
	if _, err := EventIDHash(event.EventID); err != nil {
		t.Fatal(err)
	}
	if jobID, err := JobID(fingerprint, "activation_opportunity"); err != nil ||
		jobID != "16eceb6e98b17772132357fe68bec43f5f2678307bd4192ccc2201affa48c968" {
		t.Fatalf("Python scheduler job identity differs: %s, %v", jobID, err)
	}
	event.OccurredAt = time.Date(2026, 10, 24, 4, 0, 0, 123_000_000, time.UTC)
	plan, err = DefaultPlan(event)
	if err != nil {
		t.Fatal(err)
	}
	record, err = plan.Record()
	if err != nil || record["activation_due_at"] != "2026-10-24T04:00:00.123000+00:00" ||
		record["plan_sha256"] != "365f21a0d1acb0283f38dec68c8e88d00fc6f35ba790f9d2db88adac64aae1c6" {
		t.Fatalf("Python microsecond plan differs: %+v, %v", record, err)
	}
}

func TestHostStateRejectsContentAndUnknownTransitions(t *testing.T) {
	base := HostStateEvent{EventID: "event:1", EventType: "tool_result", WorkspaceID: "alpha",
		RunID: "run:1", StageID: "stage:1", StateRevision: "bundle:1", OccurredAt: time.Now()}
	bad := base
	bad.SignalCodes = []string{"raw prompt text"}
	if _, err := bad.Fingerprint(); err == nil {
		t.Fatal("host content entered scheduler event")
	}
	bad = base
	bad.EventType = "execute_tool"
	if _, err := bad.Fingerprint(); err == nil {
		t.Fatal("unknown host-state transition accepted")
	}
}
