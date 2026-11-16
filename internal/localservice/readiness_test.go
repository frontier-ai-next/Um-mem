package localservice

import (
	"context"
	"testing"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/frontier-ai-next/Um-mem/internal/scheduler"
	"github.com/frontier-ai-next/Um-mem/internal/servicepolicy"
	"github.com/frontier-ai-next/Um-mem/internal/servicequeue"
)

func TestConfiguredListenerReadinessAndQueueDegradation(t *testing.T) {
	root := t.TempDir()
	identity := Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team", Backend: "mnemosyne"}
	selected, err := engine.Open(engine.Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	server, err := New(identity, nil, EngineDispatcher(selected))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := servicepolicy.SnapshotFromBlocks(selected.Blocks())
	if err != nil {
		t.Fatal(err)
	}
	policy, err := servicepolicy.NewStore(servicepolicy.Identity{RepoRoot: root,
		WorkspaceID: "alpha", ProfileID: "team", Backend: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.EnsureInitialized(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := server.ConfigurePolicy(policy); err != nil {
		t.Fatal(err)
	}
	schedulerPath, digest, err := SchedulerPath(identity)
	if err != nil {
		t.Fatal(err)
	}
	scheduled, err := scheduler.Open(schedulerPath, digest, "alpha", selected.PlanHostState)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduled.Close()
	if err := server.ConfigureScheduler(scheduled); err != nil {
		t.Fatal(err)
	}
	workPath, err := WorkQueuePath(identity)
	if err != nil {
		t.Fatal(err)
	}
	work, err := servicequeue.Open(workPath, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer work.Close()
	if err := server.ConfigureWorkQueue(work); err != nil {
		t.Fatal(err)
	}
	if err := server.ConfigurePanel(selected); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, done, err := server.StartListener(ctx, 0); err != nil {
		t.Fatal(err)
	} else {
		defer func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("listener did not shut down")
			}
		}()
	}
	if health := server.health(); health["readiness"] != "not_ready" {
		t.Fatalf("listener became ready before background workers: %+v", health)
	}
	server.SetBackgroundReady(true)
	if health := server.health(); health["ok"] != true || health["readiness"] != "ready" ||
		health["state"] != "ready" || health["native_health"] != "healthy" {
		t.Fatalf("configured native listener did not become ready: %+v", health)
	}
	server.mu.Lock()
	server.workQueueErr = true
	server.mu.Unlock()
	if health := server.health(); health["readiness"] != "ready" ||
		health["state"] != "ready_read_only" || health["degraded_state"] != "background_queue_unavailable" {
		t.Fatalf("queue failure did not produce read-only readiness: %+v", health)
	}
	server.mu.Lock()
	server.leases["read-only-test"] = lease{role: "operator", sessionID: "session-1",
		expires: time.Now().Add(time.Minute)}
	server.mu.Unlock()
	call := frame()
	call["lease"], call["caller_role"], call["session_id"] = "read-only-test", "operator", "session-1"
	call["tool"], call["arguments"] = "remember", map[string]any{}
	if result := post(t, server, "/call", call); result["error"] != "local service emergency read-only" {
		t.Fatalf("read-only listener accepted a mutation: %+v", result)
	}
	server.BeginShutdown()
	if health := server.health(); health["readiness"] != "not_ready" || health["state"] != "draining" {
		t.Fatalf("draining listener retained readiness: %+v", health)
	}
}
