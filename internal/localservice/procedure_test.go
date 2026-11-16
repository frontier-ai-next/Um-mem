package localservice

import (
	"context"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/frontier-ai-next/Um-mem/internal/mcpserver"
)

func TestDispatcherProcedureUsesCanonicalWorkspaceAndLeaseRun(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	selected, err := engine.Open(engine.Config{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	created, err := mcpserver.RememberPayload(ctx, selected, mcpserver.RememberArgs{
		Content: "Restart the local service after changing its configuration.",
		Title:   "Restart local service", Kind: "procedure",
		RequestedBlockID: "stable_project_facts", SessionID: "session-1", RunID: "session-1",
		Metadata: map[string]any{"steps": []any{"stop", "start"}},
	}, "alpha", "mnemosyne", "parent", 1)
	if err != nil || created["saved"] != true {
		t.Fatalf("procedure setup failed: %+v, %v", created, err)
	}
	caller := Caller{Identity: Identity{RepoRoot: root, WorkspaceID: "alpha",
		ProfileID: "team", Backend: "mnemosyne"}, Role: "parent",
		Lineage: Lineage{WorkspaceID: "alpha", SessionID: "session-2"}}
	dispatch := EngineDispatcher(selected)
	result := dispatch(ctx, caller, "procedure", map[string]any{"query": "restart local service"})
	if result["ok"] != true || result["count"] != 1 {
		t.Fatalf("service procedure read lost durable candidate: %+v", result)
	}
	entry := result["procedures"].([]map[string]any)[0]
	if entry["record_id"] != created["memory_id"] || entry["freshness"] != "unverified" {
		t.Fatalf("service procedure response lost identity or freshness: %+v", entry)
	}
	if spoofed := dispatch(ctx, caller, "procedure", map[string]any{"query": "restart local service", "workspace_id": "foreign"}); spoofed["workspace_id"] != "alpha" {
		t.Fatalf("procedure escaped the selected workspace: %+v", spoofed)
	}
	if bad := dispatch(ctx, caller, "procedure", map[string]any{"query": "restart", "scope_chain": []any{"workspace:foreign"}}); bad["ok"] != false {
		t.Fatalf("procedure accepted an unsupported scope selector: %+v", bad)
	}
}
