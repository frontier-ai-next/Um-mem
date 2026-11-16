package localservice

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestLiveLoopbackListenerAndRegistry(t *testing.T) {
	identity := Identity{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", Backend: "hindsight"}
	secrets, err := LoadOrCreateBootstrap(identity)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := New(identity, secrets, func(_ context.Context, caller Caller, tool string, args map[string]any) map[string]any {
		if caller.Role != "operator" || tool != "read_exact_record" || args["record_id"] != "record-1" {
			t.Errorf("live listener lost trusted caller identity: %+v %q %+v", caller, tool, args)
		}
		return map[string]any{"ok": true, "record_id": "record-1"}
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	address, done, err := selected.StartListener(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := Paths(identity)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.Registry)
	if err != nil {
		t.Fatal(err)
	}
	var registry registryFile
	if err := json.Unmarshal(data, &registry); err != nil || registry.Port < 1 || registry.PID != os.Getpid() {
		t.Fatalf("live listener registry is invalid: %+v, %v", registry, err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	postLive := func(path string, payload any) map[string]any {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Post("http://"+address+path, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("listener returned HTTP %d", response.StatusCode)
		}
		var result map[string]any
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if health := postLive("/health", frame()); health["readiness"] != "not_ready" {
		t.Fatalf("partial service advertised readiness: %+v", health)
	}
	leaseRequest := frame()
	leaseRequest["caller_role"] = "operator"
	leaseRequest["session_id"] = "session-1"
	leaseRequest["bootstrap_secret"] = secrets["operator"]
	leaseRequest["capabilities"] = selected.capabilities["operator"]
	issued := postLive("/lease", leaseRequest)
	token, ok := issued["lease"].(string)
	if !ok || token == "" {
		t.Fatalf("live listener did not issue role-bound lease: %+v", issued)
	}
	request := frame()
	request["lease"], request["caller_role"], request["session_id"] = token, "operator", "session-1"
	request["tool"] = "read_exact_record"
	request["arguments"] = map[string]any{"record_id": "record-1"}
	if result := postLive("/call", request); result["record_id"] != "record-1" {
		t.Fatalf("live listener did not route an authorized call: %+v", result)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("local service listener did not drain")
	}
	if _, err := os.Stat(paths.Registry); !os.IsNotExist(err) {
		t.Fatalf("listener registry survived shutdown: %v", err)
	}
}
