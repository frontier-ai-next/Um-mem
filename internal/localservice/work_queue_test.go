package localservice

import (
	"context"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/servicequeue"
)

func TestWorkQueueBindingAndHealthStayFailClosed(t *testing.T) {
	root := t.TempDir()
	identity := Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team", Backend: "mnemosyne"}
	server, err := New(identity, nil, func(context.Context, Caller, string, map[string]any) map[string]any {
		return map[string]any{"ok": true}
	})
	if err != nil {
		t.Fatal(err)
	}
	path, err := WorkQueuePath(identity)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := servicequeue.Open(path, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if err := server.ConfigureWorkQueue(queue); err != nil {
		t.Fatal(err)
	}
	health := server.health()
	background, ok := health["background_queue"].(map[string]any)
	if !ok || background["available"] != true || background["no_silent_drop"] != true ||
		health["readiness"] != "not_ready" {
		t.Fatalf("partial service queue health: %#v", health)
	}
	if lanes, ok := background["lanes"].([]servicequeue.LaneHealth); !ok || len(lanes) != 5 {
		t.Fatalf("five queue lanes were not reported: %#v", background)
	}
	foreign := identity
	foreign.Backend = "zvec"
	foreignPath, err := WorkQueuePath(foreign)
	if err != nil {
		t.Fatal(err)
	}
	foreignQueue, err := servicequeue.Open(foreignPath, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer foreignQueue.Close()
	if err := server.ConfigureWorkQueue(foreignQueue); err == nil {
		t.Fatal("foreign work queue was attached to listener")
	}
}

func TestIndexQueueBackpressureAndAdmissionFailureAreVisible(t *testing.T) {
	root := t.TempDir()
	identity := Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team", Backend: "mnemosyne"}
	server, err := New(identity, nil, func(context.Context, Caller, string, map[string]any) map[string]any {
		return map[string]any{"ok": true}
	})
	if err != nil {
		t.Fatal(err)
	}
	path, err := WorkQueuePath(identity)
	if err != nil {
		t.Fatal(err)
	}
	policies := make(map[string]servicequeue.Policy, len(servicequeue.DefaultPolicies))
	for lane, policy := range servicequeue.DefaultPolicies {
		policies[lane] = policy
	}
	policies["indexing"] = servicequeue.Policy{Capacity: 1, TargetLatencyMS: 5000}
	queue, err := servicequeue.Open(path, policies, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ConfigureWorkQueue(queue); err != nil {
		t.Fatal(err)
	}
	first := map[string]any{"ok": true, "memory_id": "record-1"}
	server.enqueueIndexRefresh(context.Background(), first)
	if _, reported := first["background_work"]; reported {
		t.Fatalf("successful admission reported a warning: %#v", first)
	}
	second := map[string]any{"ok": true, "memory_id": "record-2"}
	server.enqueueIndexRefresh(context.Background(), second)
	background, ok := second["background_work"].(map[string]any)
	if !ok || background["status"] != "backpressured" || background["no_silent_drop"] != true {
		t.Fatalf("critical backpressure was hidden: %#v", second)
	}
	health := server.health()["background_queue"].(map[string]any)
	if health["emergency_read_only"] != true || health["available"] != true {
		t.Fatalf("critical queue state was hidden: %#v", health)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	third := map[string]any{"ok": true, "memory_id": "record-3"}
	server.enqueueIndexRefresh(context.Background(), third)
	failed, ok := third["background_work"].(map[string]any)
	if !ok || failed["status"] != "admission_failed" || failed["no_silent_drop"] != false {
		t.Fatalf("queue failure was hidden: %#v", third)
	}
	health = server.health()["background_queue"].(map[string]any)
	if health["available"] != false {
		t.Fatalf("queue failure did not persist in health: %#v", health)
	}
}
