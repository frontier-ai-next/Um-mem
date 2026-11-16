package localservice

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/catalog"
	"github.com/frontier-ai-next/Um-mem/internal/engine"
)

func TestSupervisorServiceReadsPendingTopologyJournal(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := engine.Open(engine.Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	path, err := catalog.Path(root, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	payload := `{"assignments":{},"operation":"merge","sources":{},"targets":[]}`
	digest := sha256.Sum256([]byte(payload))
	if _, err := db.ExecContext(ctx, `INSERT INTO topology_evolutions(
		operation_id,workspace_id,profile_id,state,payload_json,payload_sha256,
		abort_reason,created_at,updated_at) VALUES(?,?,?,?,?,?,'',?,?)`,
		"merge-1", "alpha", "team", "projections_ready", payload,
		hex.EncodeToString(digest[:]), "2026-01-01", "2026-01-01"); err != nil {
		t.Fatal(err)
	}
	server, err := New(Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team",
		Backend: "mnemosyne"}, map[string]string{"supervisor": "supervisor-secret"},
		EngineDispatcher(selected))
	if err != nil {
		t.Fatal(err)
	}
	lease := frame()
	lease["caller_role"], lease["session_id"] = "supervisor", "session-1"
	lease["bootstrap_secret"] = "supervisor-secret"
	lease["scope_lineage"] = map[string]any{"workspace_id": "alpha", "session_id": "session-1"}
	lease["capabilities"] = server.capabilities["supervisor"]
	granted := post(t, server, "/lease", lease)
	token, ok := granted["lease"].(string)
	if granted["ok"] != true || !ok {
		t.Fatalf("supervisor lease failed: %+v", granted)
	}
	call := frame()
	call["caller_role"], call["session_id"], call["lease"] = "supervisor", "session-1", token
	call["tool"] = "pending_topology_evolutions"
	call["arguments"] = map[string]any{}
	result := post(t, server, "/call", call)
	pending, ok := result["pending"].([]any)
	if result["ok"] != true || !ok || len(pending) != 1 ||
		pending[0].(map[string]any)["operation_id"] != "merge-1" ||
		pending[0].(map[string]any)["state"] != "projections_ready" {
		t.Fatalf("service pending topology journal differs: %+v", result)
	}
	call["arguments"] = map[string]any{"profile_id": "other"}
	if forged := post(t, server, "/call", call); forged["ok"] != false {
		t.Fatalf("service accepted caller-selected topology profile: %+v", forged)
	}
	call["tool"] = "abort_topology_evolution"
	call["arguments"] = map[string]any{"operation_id": "merge-1",
		"reason": "native projection failed"}
	aborted := post(t, server, "/call", call)
	if aborted["ok"] != true || aborted["state"] != "aborted" ||
		aborted["reason"] != "native projection failed" {
		t.Fatalf("service topology abort failed: %+v", aborted)
	}
	call["tool"] = "pending_topology_evolutions"
	call["arguments"] = map[string]any{}
	after := post(t, server, "/call", call)
	if after["ok"] != true || len(after["pending"].([]any)) != 0 {
		t.Fatalf("aborted topology remains pending over service: %+v", after)
	}
	call["tool"] = "resume_topology_evolution"
	call["arguments"] = map[string]any{"operation_id": "merge-1"}
	if resumed := post(t, server, "/call", call); resumed["ok"] != false {
		t.Fatalf("service resumed an aborted topology mapping: %+v", resumed)
	}
	for _, tool := range []string{"split_block", "merge_blocks"} {
		call["tool"], call["arguments"] = tool, map[string]any{"profile_id": "other"}
		if forged := post(t, server, "/call", call); forged["ok"] != false {
			t.Fatalf("service accepted caller-selected topology scope for %s: %+v", tool, forged)
		}
	}
}
