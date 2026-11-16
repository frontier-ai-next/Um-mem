package localservice

import (
	"context"
	"encoding/json"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/scheduler"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/servicepolicy"
)

func TestHostStateRolePolicyAndDurableReplay(t *testing.T) {
	root := t.TempDir()
	identity := Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team", Backend: "hindsight"}
	policy, err := servicepolicy.NewStore(servicepolicy.Identity{RepoRoot: root,
		WorkspaceID: "alpha", ProfileID: "team", Backend: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := servicepolicy.NormalizeSnapshot([]map[string]any{{"block_id": "example"}})
	if _, err := policy.EnsureInitialized(snapshot); err != nil {
		t.Fatal(err)
	}
	path, digest, err := SchedulerPath(identity)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := scheduler.Open(path, digest, "alpha", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	server, err := New(identity, map[string]string{"host_state": "state-secret", "operator": "operator-secret"},
		func(context.Context, Caller, string, map[string]any) map[string]any {
			return map[string]any{"ok": true}
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ConfigurePolicy(policy); err != nil {
		t.Fatal(err)
	}
	if err := server.ConfigureScheduler(queue); err != nil {
		t.Fatal(err)
	}
	leaseRequest := frame()
	leaseRequest["caller_role"], leaseRequest["session_id"] = "host_state", "session-1"
	leaseRequest["bootstrap_secret"] = "state-secret"
	leaseRequest["capabilities"] = server.capabilities["host_state"]
	issued := post(t, server, "/lease", leaseRequest)
	leaseToken, ok := issued["lease"].(string)
	if issued["ok"] != true || !ok {
		t.Fatalf("host-state lease failed: %+v", issued)
	}
	request := frame()
	request["protocol_version"] = json.Number("1")
	request["schema_version"] = json.Number("2")
	request["api_version"] = json.Number("2")
	request["lease"], request["caller_role"], request["session_id"] = leaseToken, "host_state", "session-1"
	request["event"] = map[string]any{"event_id": "codex-host-state:abc",
		"event_type": "prospective_trigger", "run_id": "codex-session:abc",
		"stage_id": "codex-turn:def", "signal_codes": []any{"schema:1"}}
	if actual := post(t, server, "/host-state", request); actual["error"] != "local service listener is not ready" {
		t.Fatalf("partial listener accepted production host state: %+v", actual)
	}
	first := server.submitHostStateAtReadiness(context.Background(), request, true)
	if first["ok"] != true || first["replayed"] != false ||
		first["policy_bundle_revision"] != issued["policy_bundle_revision"] ||
		len(first["jobs"].([]map[string]any)) != 2 {
		t.Fatalf("typed host state was not durably scheduled: %+v", first)
	}
	second := server.submitHostStateAtReadiness(context.Background(), request, true)
	if second["ok"] != true || second["replayed"] != true ||
		second["event_fingerprint_sha256"] != first["event_fingerprint_sha256"] {
		t.Fatalf("host state replay created a new decision: %+v", second)
	}
	invalid := clone(request)
	invalid["event"] = clone(request["event"].(map[string]any))
	invalid["event"].(map[string]any)["prompt"] = "private user text"
	if actual := server.submitHostStateAtReadiness(context.Background(), invalid, true); actual["ok"] != false {
		t.Fatalf("raw host content entered the scheduler: %+v", actual)
	}
	invalid = clone(request)
	invalid["session_id"] = "foreign-session"
	if actual := server.submitHostStateAtReadiness(context.Background(), invalid, true); actual["ok"] != false {
		t.Fatalf("foreign session used a host-state lease: %+v", actual)
	}
}
