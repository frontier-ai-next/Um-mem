package mcpserver

import (
	"context"
	"errors"
	"os"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func boundMCPExportScope(workspaceID string, requested []string) ([]string, error) {
	identity := map[string]string{"subtask": os.Getenv("UM_MEM_SUBTASK_ID"),
		"task": os.Getenv("UM_MEM_TASK_ID"), "session": os.Getenv("UM_MEM_SESSION_ID"),
		"workspace": workspaceID, "project": os.Getenv("UM_MEM_PROJECT_ID"),
		"user_global": os.Getenv("UM_MEM_USER_ID")}
	order := []string{"subtask", "task", "session", "workspace", "project", "user_global"}
	if len(requested) == 0 {
		chain := []string{}
		for _, kind := range order[:5] {
			if id := strings.TrimSpace(identity[kind]); id != "" {
				chain = append(chain, kind+":"+id)
			}
		}
		return chain, nil
	}
	seen := map[string]bool{}
	previous := -1
	for _, key := range requested {
		kind, id, found := strings.Cut(key, ":")
		rank := -1
		for i, item := range order {
			if item == kind {
				rank = i
				break
			}
		}
		if !found || rank < previous || rank < 0 || seen[kind] || id == "" || id == "*" ||
			key != strings.TrimSpace(key) || identity[kind] == "" || identity[kind] != id {
			return nil, errors.New("scope chain is not bound to MCP host lineage")
		}
		seen[kind] = true
		previous = rank
	}
	return requested, nil
}

type ExportObsidianArgs struct {
	VaultDir     string   `json:"vault_dir,omitempty"`
	ProjectTitle string   `json:"project_title,omitempty"`
	ScopeChain   []string `json:"scope_chain,omitempty"`
}

func addExportObsidianTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string) {
	mcp.AddTool(server, &mcp.Tool{Name: "export_obsidian",
		Description: "Export profile-visible, scoped core memory and evidence as an Obsidian vault."},
		func(ctx context.Context, _ *mcp.CallToolRequest, args ExportObsidianArgs) (*mcp.CallToolResult, map[string]any, error) {
			chain, err := boundMCPExportScope(workspaceID, args.ScopeChain)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			vault, err := selected.AuthorizedObsidianVault(args.VaultDir)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			result, err := selected.ExportObsidian(ctx, vault, args.ProjectTitle, chain)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, result, nil
		})
}
