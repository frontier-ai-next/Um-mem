package scheduler

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func schedulerEvent() HostStateEvent {
	return HostStateEvent{EventID: "codex-host-state:abc", EventType: "prospective_trigger",
		WorkspaceID: "alpha", RunID: "codex-session:abc", StageID: "codex-turn:abc",
		StateRevision: "bundle:1", OccurredAt: time.Date(2026, 10, 24, 4, 0, 0, 0, time.UTC),
		SignalCodes:     []string{"schema:1", "codex_event:UserPromptSubmit"},
		CapabilityCodes: []string{"codex_hook", "typed_host_state"}}
}

func TestSchedulerPersistsReplayAndAdvisoryReceipts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "scheduler.sqlite")
	identity := hash("service-identity:alpha")
	store, err := Open(path, identity, "alpha", nil)
	if err != nil {
		t.Fatal(err)
	}
	event := schedulerEvent()
	first, err := store.Submit(ctx, event)
	if err != nil || first.Replayed || len(first.Jobs) != 2 {
		t.Fatalf("first scheduler submission failed: %+v, %v", first, err)
	}
	if first.PlanSHA256 != "a9378b30107a021492e20af719fcabd58045444f88f1423f804169119d8ab285" {
		t.Fatalf("plan digest changed: %s", first.PlanSHA256)
	}
	if pending, err := store.HasPending(ctx); err != nil || !pending {
		t.Fatalf("scheduler lost pending jobs: %v, %v", pending, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, identity, "alpha", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	replay, err := store.Submit(ctx, event)
	if err != nil || !replay.Replayed || replay.PlanSHA256 != first.PlanSHA256 {
		t.Fatalf("durable scheduler replay changed: %+v, %v", replay, err)
	}
	changed := event
	changed.SignalCodes = []string{"schema:2"}
	if _, err := store.Submit(ctx, changed); err == nil {
		t.Fatal("changed event reused an event ID")
	}
	due, err := store.RecoverDue(ctx, event.OccurredAt.Add(time.Minute))
	if err != nil || len(due) != 2 {
		t.Fatalf("due work was lost: %+v, %v", due, err)
	}
	receipts, err := store.FireDue(ctx, event.OccurredAt.Add(time.Minute))
	if err != nil || len(receipts) != 2 {
		t.Fatalf("advisory receipt write failed: %+v, %v", receipts, err)
	}
	if receipts[0].Executed || receipts[1].Executed {
		t.Fatal("advisory scheduler claimed host execution")
	}
	if again, err := store.FireDue(ctx, event.OccurredAt.Add(2*time.Minute)); err != nil || len(again) != 0 {
		t.Fatalf("scheduler resolved a job twice: %+v, %v", again, err)
	}
	if pending, err := store.HasPending(ctx); err != nil || pending {
		t.Fatalf("resolved job stayed pending: %v, %v", pending, err)
	}
	for _, receipt := range receipts {
		stored, err := store.ReceiptFor(ctx, receipt.JobID)
		if err != nil || stored == nil || stored.Outcome != receipt.Outcome || stored.Executed {
			t.Fatalf("advisory receipt changed after restart: %+v, %v", stored, err)
		}
	}
}

func TestSchedulerRefusesForeignIdentityAndWorkspace(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "scheduler.sqlite")
	store, err := Open(path, hash("first"), "alpha", nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign := schedulerEvent()
	foreign.WorkspaceID = "other"
	if _, err := store.Submit(ctx, foreign); err == nil {
		t.Fatal("foreign workspace entered scheduler")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, hash("second"), "alpha", nil); err == nil {
		t.Fatal("foreign identity opened scheduler state")
	}
}
