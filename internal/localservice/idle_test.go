package localservice

import (
	"context"
	"testing"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/scheduler"
	"github.com/frontier-ai-next/Um-mem/internal/servicequeue"
)

func TestIdleShutdownWaitsForDurableWorkAndActiveRequests(t *testing.T) {
	ctx := context.Background()
	identity := Identity{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", Backend: "mnemosyne"}
	server, err := New(identity, nil, func(context.Context, Caller, string, map[string]any) map[string]any {
		return map[string]any{"ok": true}
	})
	if err != nil {
		t.Fatal(err)
	}
	queuePath, err := WorkQueuePath(identity)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := servicequeue.Open(queuePath, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if err := server.ConfigureWorkQueue(queue); err != nil {
		t.Fatal(err)
	}
	schedulerPath, digest, err := SchedulerPath(identity)
	if err != nil {
		t.Fatal(err)
	}
	scheduled, err := scheduler.Open(schedulerPath, digest, "alpha", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduled.Close()
	if err := server.ConfigureScheduler(scheduled); err != nil {
		t.Fatal(err)
	}
	server.lastWork = time.Now().Add(-10 * time.Minute)
	if !server.IsIdle(ctx, time.Minute) {
		t.Fatal("empty detached listener did not become idle")
	}
	server.activeRequests = 1
	if server.IsIdle(ctx, time.Minute) {
		t.Fatal("listener stopped during active request")
	}
	server.activeRequests = 0
	if _, err := queue.Submit(ctx, "indexing", "record-1", "record-1", 3); err != nil {
		t.Fatal(err)
	}
	if server.IsIdle(ctx, time.Minute) {
		t.Fatal("listener stopped with pending durable work")
	}
}
