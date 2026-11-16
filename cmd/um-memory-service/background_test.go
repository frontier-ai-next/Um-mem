package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/scheduler"
	"github.com/frontier-ai-next/Um-mem/internal/servicequeue"
)

func TestDueSchedulerJobsEnterTheirDurableLanes(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	identityHash := sha256.Sum256([]byte("test-service-identity"))
	scheduled, err := scheduler.Open(filepath.Join(root, "scheduler.sqlite"),
		hex.EncodeToString(identityHash[:]), "alpha", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduled.Close()
	work, err := servicequeue.Open(filepath.Join(root, "work.sqlite"), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer work.Close()
	event := scheduler.HostStateEvent{EventID: "codex-host-state:abc", EventType: "prospective_trigger",
		WorkspaceID: "alpha", RunID: "codex-session:abc", StageID: "codex-turn:abc",
		StateRevision: "bundle:1", OccurredAt: time.Now().Add(-time.Minute),
		SignalCodes:     []string{"schema:1", "codex_event:UserPromptSubmit"},
		CapabilityCodes: []string{"codex_hook", "typed_host_state"}}
	if _, err := scheduled.Submit(ctx, event); err != nil {
		t.Fatal(err)
	}
	queued, err := enqueueDueSchedulerWork(ctx, scheduled, work, time.Now())
	if err != nil || queued != 2 {
		t.Fatalf("scheduler admission: %d, %v", queued, err)
	}
	queued, err = enqueueDueSchedulerWork(ctx, scheduled, work, time.Now())
	if err != nil || queued != 0 {
		t.Fatalf("scheduler replay duplicated queue work: %d, %v", queued, err)
	}
	health, err := work.Health(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	lanes := map[string]servicequeue.LaneHealth{}
	for _, lane := range health.Lanes {
		lanes[lane.Lane] = lane
	}
	if lanes["prospective_scheduler"].Pending != 1 ||
		lanes["consolidation_transition"].Pending != 1 {
		t.Fatalf("scheduler work entered wrong lanes: %#v", lanes)
	}
}
