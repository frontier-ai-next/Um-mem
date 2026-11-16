package localservice

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
)

func TestObsidianExportBindsServiceRoleAndScope(t *testing.T) {
	repo := t.TempDir()
	selected, err := engine.Open(engine.Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "team", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	dispatch := EngineDispatcher(selected)
	caller := Caller{Identity: Identity{RepoRoot: repo, WorkspaceID: "alpha", ProfileID: "team", Backend: "mnemosyne"},
		Role: "child", Lineage: Lineage{WorkspaceID: "alpha", SessionID: "session-1"}}
	vault := filepath.Join(repo, "vault")
	t.Setenv("UM_MEM_OBSIDIAN_VAULT", vault)
	args := map[string]any{"vault_dir": vault, "scope_chain": []any{"workspace:alpha"}}
	if result := dispatch(context.Background(), caller, "export_obsidian", args); result["ok"] != false {
		t.Fatalf("child exported memory: %+v", result)
	}
	caller.Role = "operator"
	args["vault_dir"] = filepath.Join(repo, "other-vault")
	if result := dispatch(context.Background(), caller, "export_obsidian", args); result["ok"] != false {
		t.Fatalf("export accepted unconfigured vault: %+v", result)
	}
	args["vault_dir"] = vault
	args["scope_chain"] = []any{"workspace:other"}
	if result := dispatch(context.Background(), caller, "export_obsidian", args); result["ok"] != false {
		t.Fatalf("export accepted forged workspace scope: %+v", result)
	}
	args["scope_chain"] = []any{"workspace:alpha"}
	result := dispatch(context.Background(), caller, "export_obsidian", args)
	if result["ok"] != true || result["workspace_id"] != "alpha" {
		t.Fatalf("operator export failed: %+v", result)
	}
	if _, err := os.Stat(result["index_path"].(string)); err != nil {
		t.Fatal(err)
	}
}
