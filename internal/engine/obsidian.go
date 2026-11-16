package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/obsidian"
)

func (engine *Engine) defaultObsidianVault() string {
	if value := strings.TrimSpace(os.Getenv("UM_MEM_OBSIDIAN_VAULT")); value != "" {
		return value
	}
	return filepath.Join(engine.config.RepoRoot, "exports", "obsidian")
}

func resolvedVaultPath(value string) (string, error) {
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, strings.TrimLeft(value[1:], `/\`))
	}
	path, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	missing := []string{}
	ancestor := path
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", errors.New("vault path has no existing ancestor")
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
	}
	ancestor, err = filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		ancestor = filepath.Join(ancestor, missing[i])
	}
	return filepath.Clean(ancestor), nil
}

// AuthorizedObsidianVault enforces the host-configured export destination for
// untrusted MCP and local-service calls. The CLI may choose a vault directly.
func (engine *Engine) AuthorizedObsidianVault(requested string) (string, error) {
	expected := engine.defaultObsidianVault()
	if requested == "" {
		return expected, nil
	}
	want, err := resolvedVaultPath(expected)
	if err != nil {
		return "", err
	}
	actual, err := resolvedVaultPath(requested)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(actual, want) {
		return "", errors.New("export path is not authorized")
	}
	return expected, nil
}

func (engine *Engine) ExportObsidian(ctx context.Context, vaultDir, projectTitle string,
	scopeChain []string) (map[string]any, error) {
	if vaultDir == "" {
		vaultDir = engine.defaultObsidianVault()
	}
	records, edges, _, err := engine.index.ExportGraphSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	result, err := obsidian.Write(vaultDir, engine.config.WorkspaceID, projectTitle,
		scopeChain, records, edges)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "workspace_id": engine.config.WorkspaceID,
		"backend": engine.config.BackendID, "vault_dir": result.VaultDir,
		"index_path": result.IndexPath, "memory_overview_path": result.MemoryOverviewPath,
		"blocks_exported": result.BlocksExported, "files_written": result.FilesWritten}, nil
}
