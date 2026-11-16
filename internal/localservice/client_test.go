package localservice

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClientUsesPublishedLoopbackIdentityAndRoleLease(t *testing.T) {
	identity := Identity{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", Backend: "hindsight"}
	secrets, err := LoadOrCreateBootstrap(identity)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(identity, secrets, func(_ context.Context, caller Caller, tool string, args map[string]any) map[string]any {
		if caller.Role != "operator" || caller.Lineage.SessionID != "session-1" || tool != "read_exact_record" ||
			args["record_id"] != "record-1" {
			t.Errorf("client lost its authenticated role or request: %+v %s %+v", caller, tool, args)
		}
		return map[string]any{"ok": true, "record_id": "record-1"}
	})
	if err != nil {
		t.Fatal(err)
	}
	ready := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/health" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "liveness": "live",
				"readiness": "ready", "native_health": "healthy", "workspace_id": "alpha",
				"profile_id": "team", "selected_backend": "hindsight", "transport": "loopback_http"})
			return
		}
		server.ServeHTTP(writer, request)
	})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewUnstartedServer(ready)
	httpServer.Listener.Close()
	httpServer.Listener = listener
	httpServer.Start()
	defer httpServer.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	paths, err := writeRegistry(identity, port, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer clearRegistry(paths, os.Getpid(), port)
	client, err := NewClient(identity, "operator", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.Call(context.Background(), "session-1", Lineage{}, "read_exact_record",
		map[string]any{"record_id": "record-1"}, "")
	if err != nil || result["ok"] != true || result["record_id"] != "record-1" {
		t.Fatalf("role-bound loopback call failed: %+v %v", result, err)
	}
	panel, err := NewPanel()
	if err != nil {
		t.Fatal(err)
	}
	server.mu.Lock()
	server.panel, server.listenerPort = panel, port
	server.mu.Unlock()
	dashboard, err := client.Dashboard(context.Background(), false)
	if err != nil || dashboard["ok"] != true ||
		!strings.HasPrefix(dashboard["url"].(string), "http://127.0.0.1:") {
		t.Fatalf("operator dashboard activation failed: %+v %v", dashboard, err)
	}
	stopped, err := client.Dashboard(context.Background(), true)
	if err != nil || stopped["state"] != "stopped" || panel.isActive() {
		t.Fatalf("operator dashboard stop failed: %+v %v", stopped, err)
	}
	foreign := identity
	foreign.WorkspaceID = "foreign"
	if _, err := NewClient(foreign, "operator", time.Second); err == nil {
		t.Fatal("foreign workspace reused listener registry")
	}
}

func TestDashboardClientRespectsListenerReadiness(t *testing.T) {
	identity := Identity{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", Backend: "mnemosyne"}
	secrets, err := LoadOrCreateBootstrap(identity)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(identity, secrets, func(context.Context, Caller, string, map[string]any) map[string]any {
		return map[string]any{"ok": true}
	})
	if err != nil {
		t.Fatal(err)
	}
	panel, err := NewPanel()
	if err != nil {
		t.Fatal(err)
	}
	server.panel = panel
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, done, err := server.StartListener(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); <-done }()
	client, err := NewClient(identity, "operator", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.Dashboard(context.Background(), false)
	if err != nil || result["ok"] != false || panel.isActive() {
		t.Fatalf("partial listener opened browser panel: %+v %v", result, err)
	}
}
