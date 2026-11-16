package localservice

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/servicepolicy"
)

func post(t *testing.T, server *Server, path string, payload any) map[string]any {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.RemoteAddr = "127.0.0.1:41835"
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status %d: %s", response.Code, response.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestLeasePinsPersistedPolicyRevision(t *testing.T) {
	root := t.TempDir()
	identity := Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team", Backend: "hindsight"}
	policy, err := servicepolicy.NewStore(servicepolicy.Identity{RepoRoot: root,
		WorkspaceID: "alpha", ProfileID: "team", Backend: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := servicepolicy.NormalizeSnapshot([]map[string]any{{"block_id": "example"}})
	bundle, err := policy.EnsureInitialized(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(identity, map[string]string{"host_state": "state-secret"},
		func(_ context.Context, caller Caller, _ string, _ map[string]any) map[string]any {
			return map[string]any{"ok": true, "policy": caller.PolicyBundleRevision}
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ConfigurePolicy(policy); err != nil {
		t.Fatal(err)
	}
	request := frame()
	request["caller_role"], request["session_id"] = "host_state", "session-1"
	request["bootstrap_secret"] = "state-secret"
	request["capabilities"] = server.capabilities["host_state"]
	issued := post(t, server, "/lease", request)
	if issued["ok"] != true || issued["policy_bundle_revision"] != bundle.RevisionID ||
		issued["catalog_revision"] != bundle.CatalogRevisionID {
		t.Fatalf("lease did not pin policy bundle: %+v", issued)
	}
	pinned, err := policy.Pinned("session-1")
	if err != nil || pinned == nil || pinned.RevisionID != bundle.RevisionID {
		t.Fatalf("listener did not persist the session pin: %+v, %v", pinned, err)
	}
	foreign, err := servicepolicy.NewStore(servicepolicy.Identity{RepoRoot: root,
		WorkspaceID: "other", ProfileID: "team", Backend: "hindsight"})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ConfigurePolicy(foreign); err == nil {
		t.Fatal("listener accepted a foreign policy store")
	}
}

func frame() map[string]any {
	return map[string]any{"protocol_version": ProtocolVersion, "schema_version": SchemaVersion,
		"api_version": APIVersion, "request_id": "request-1"}
}

func clone(source map[string]any) map[string]any {
	copy := make(map[string]any, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}

func TestAuthenticatedLeaseAndScopedDispatch(t *testing.T) {
	root := t.TempDir()
	callCount := 0
	server, err := New(Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team",
		Backend: "hindsight"}, map[string]string{"operator": "operator-secret", "host_hook": "hook-secret"},
		func(_ context.Context, caller Caller, tool string, args map[string]any) map[string]any {
			callCount++
			if caller.Role != "operator" || caller.Lineage.WorkspaceID != "alpha" ||
				caller.Lineage.SessionID != "session-1" || caller.Lineage.ProjectID != "project-1" ||
				tool != "read_exact_record" || args["record_id"] != "record-1" {
				t.Errorf("dispatch lost trusted identity or arguments: %+v %q %+v", caller, tool, args)
			}
			return map[string]any{"ok": true, "record_id": "record-1"}
		})
	if err != nil {
		t.Fatal(err)
	}
	leaseRequest := frame()
	leaseRequest["caller_role"] = "operator"
	leaseRequest["session_id"] = "session-1"
	leaseRequest["bootstrap_secret"] = "operator-secret"
	leaseRequest["scope_lineage"] = map[string]any{"workspace_id": "alpha", "session_id": "session-1", "project_id": "project-1"}
	leaseRequest["capabilities"] = server.capabilities["operator"]

	invalid := clone(leaseRequest)
	invalid["bootstrap_secret"] = "hook-secret"
	if response := post(t, server, "/lease", invalid); response["ok"] != false {
		t.Fatalf("wrong role credential issued a lease: %+v", response)
	}
	invalid = clone(leaseRequest)
	invalid["capabilities"] = []string{"health", "read_exact_record"}
	if response := post(t, server, "/lease", invalid); response["ok"] != false {
		t.Fatalf("reduced capability list issued a lease: %+v", response)
	}
	invalid = clone(leaseRequest)
	invalid["scope_lineage"] = map[string]any{"workspace_id": "other", "session_id": "session-1"}
	if response := post(t, server, "/lease", invalid); response["ok"] != false {
		t.Fatalf("forged scope issued a lease: %+v", response)
	}
	issued := post(t, server, "/lease", leaseRequest)
	token, ok := issued["lease"].(string)
	if issued["ok"] != true || !ok || token == "" {
		t.Fatalf("valid caller failed to obtain lease: %+v", issued)
	}
	request := frame()
	request["lease"], request["caller_role"], request["session_id"] = token, "operator", "session-1"
	request["tool"] = "read_exact_record"
	request["arguments"] = map[string]any{"record_id": "record-1", "repo_root": root,
		"workspace_id": "alpha", "backend": "hindsight"}
	if response := post(t, server, "/call", request); response["ok"] != true || callCount != 1 {
		t.Fatalf("authorized read did not dispatch: %+v, count=%d", response, callCount)
	}

	for name, mutate := range map[string]func(map[string]any){
		"role":      func(value map[string]any) { value["caller_role"] = "host_hook" },
		"session":   func(value map[string]any) { value["session_id"] = "session-2" },
		"lease":     func(value map[string]any) { value["lease"] = "guessed" },
		"tool":      func(value map[string]any) { value["tool"] = "promote" },
		"workspace": func(value map[string]any) { value["arguments"].(map[string]any)["workspace_id"] = "other" },
		"profile":   func(value map[string]any) { value["arguments"].(map[string]any)["profile_id"] = "other" },
		"scope":     func(value map[string]any) { value["arguments"].(map[string]any)["session_id"] = "session-2" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := clone(request)
			arguments := clone(request["arguments"].(map[string]any))
			candidate["arguments"] = arguments
			mutate(candidate)
			if response := post(t, server, "/call", candidate); response["ok"] != false || callCount != 1 {
				t.Fatalf("unauthorized request reached dispatcher: %+v, count=%d", response, callCount)
			}
		})
	}
	server.now = func() time.Time { return time.Now().Add(LeaseTTL + time.Second) }
	if response := post(t, server, "/call", request); response["error"] != "local service lease rejected" {
		t.Fatalf("expired lease remained valid: %+v", response)
	}
}

func TestProtocolLimitsAndReadiness(t *testing.T) {
	root := t.TempDir()
	server, err := New(Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team",
		Backend: "hindsight"}, nil, func(_ context.Context, _ Caller, _ string, _ map[string]any) map[string]any {
		return map[string]any{"ok": true}
	})
	if err != nil {
		t.Fatal(err)
	}
	health := post(t, server, "/health", map[string]any{})
	if health["readiness"] != "not_ready" || health["degraded_state"] != "go_port_partial" {
		t.Fatalf("partial listener claimed production readiness: %+v", health)
	}
	if health["slo_contract_revision"] != "service-slo:sha256:a3b9adac91fec1a5ce46b8b3c341b5597afafaf022b63651c15cfdb176fa4844" {
		t.Fatalf("service SLO contract revision is missing or incompatible: %+v", health)
	}
	invalid := frame()
	invalid["api_version"] = 3
	if response := post(t, server, "/lease", invalid); response["error"] != "local service API version mismatch" {
		t.Fatalf("mismatched protocol frame was accepted: %+v", response)
	}
	request := httptest.NewRequest(http.MethodPost, "/call", bytes.NewReader(bytes.Repeat([]byte("a"), MaxMessageBytes+1)))
	request.RemoteAddr = "127.0.0.1:41835"
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if !bytes.Contains(response.Body.Bytes(), []byte("protocol limit")) {
		t.Fatalf("oversize body did not fail closed: %s", response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".UM-MEM", "memory")); !os.IsNotExist(err) {
		t.Fatal("partial handler unexpectedly registered a listener")
	}
	request = httptest.NewRequest(http.MethodPost, "/health", bytes.NewReader([]byte(`{}`)))
	request.RemoteAddr = "192.0.2.1:41835"
	response = httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("nonlocal caller reached listener: %d", response.Code)
	}
}
