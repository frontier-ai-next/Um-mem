package mcpserver

import (
	"context"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
)

func TestManagerRecallRequiresBoundUserAndKeepsHitsAdvisory(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	manager, err := engine.Open(engine.Config{RepoRoot: repo,
		WorkspaceID: engine.ManagerWorkspaceID, ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := RememberPayload(ctx, manager, RememberArgs{
		Content:          "User prefers concise project summaries",
		RequestedBlockID: "user_global_preferences", UserID: "user-a",
		SessionID: "session-1", RunID: "session-1",
	}, engine.ManagerWorkspaceID, "mnemosyne", "parent", 1)
	if err != nil || created["saved"] != true {
		manager.Close()
		t.Fatalf("manager candidate setup failed: %+v, %v", created, err)
	}
	createdID := created["memory_id"].(string)
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	workspace, err := engine.Open(engine.Config{RepoRoot: repo,
		WorkspaceID: "alpha", ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	args := RecallArgs{Query: "concise project summaries", SearchAll: true,
		IncludeManager: true}
	if _, err := RecallPayload(ctx, workspace, args, "alpha", "mnemosyne",
		[]string{"workspace:alpha"}, "session-1"); err == nil {
		t.Fatal("unbound direct MCP call accessed manager memory")
	}
	foreign, err := RecallPayloadForUser(ctx, workspace, args, "alpha", "mnemosyne",
		[]string{"workspace:alpha"}, "session-2", "user-b")
	if err != nil || len(foreign["hits"].([]map[string]any)) != 0 {
		t.Fatalf("manager recall crossed user identity: %+v, %v", foreign, err)
	}
	allowed, err := RecallPayloadForUser(ctx, workspace, args, "alpha", "mnemosyne",
		[]string{"workspace:alpha"}, "session-2", "user-a")
	if err != nil {
		t.Fatal(err)
	}
	hits := allowed["hits"].([]map[string]any)
	if len(hits) != 1 || allowed["include_manager"] != true {
		t.Fatalf("explicit manager recall missed scoped preference: %+v", allowed)
	}
	metadata, _ := hits[0]["metadata"].(map[string]any)
	if metadata["workspace_id"] != engine.ManagerWorkspaceID ||
		metadata["authority"] != "advisory" || metadata["memory_level"] != "manager" {
		t.Fatalf("manager result escaped advisory or scope marking: %+v", hits[0])
	}
	args.RecordIDs = []string{"unrelated-record"}
	excluded, err := RecallPayloadForUser(ctx, workspace, args, "alpha", "mnemosyne",
		[]string{"workspace:alpha"}, "session-2", "user-a")
	if err != nil || len(excluded["hits"].([]map[string]any)) != 0 {
		t.Fatalf("manager recall ignored an exact record selector: %+v, %v", excluded, err)
	}
	args.RecordIDs = []string{createdID}
	exact, err := RecallPayloadForUser(ctx, workspace, args, "alpha", "mnemosyne",
		[]string{"workspace:alpha"}, "session-2", "user-a")
	if err != nil || len(exact["hits"].([]map[string]any)) != 1 {
		t.Fatalf("manager recall rejected a matching exact selector: %+v, %v", exact, err)
	}
	args.RecordIDs = nil
	args.SourceSpeakers = []string{"Ada"}
	filtered, err := RecallPayloadForUser(ctx, workspace, args, "alpha", "mnemosyne",
		[]string{"workspace:alpha"}, "session-2", "user-a")
	if err != nil || len(filtered["hits"].([]map[string]any)) != 0 {
		t.Fatalf("manager recall bypassed source speaker filter: %+v, %v", filtered, err)
	}
}
