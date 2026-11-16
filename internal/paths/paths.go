package paths

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

const ManagerWorkspaceID = "um-mem-manager"

var invalidWorkspaceCharacter = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// NormalizeWorkspaceID returns the single directory component used by a workspace.
func NormalizeWorkspaceID(workspaceID string) (string, error) {
	raw := strings.Trim(strings.ReplaceAll(strings.TrimSpace(workspaceID), `\`, "/"), "/")
	for strings.HasPrefix(strings.ToLower(raw), "./") {
		raw = strings.Trim(raw[2:], "/")
	}
	if strings.HasPrefix(strings.ToLower(raw), ".workspaces/") {
		raw = strings.Trim(raw[1:], "/")
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == '/' })
	if len(parts) > 0 && strings.EqualFold(parts[0], "workspaces") {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return "", nil
	}
	if parts[0] == ".." {
		return "", errors.New("workspace_id must not contain path traversal")
	}
	return strings.Trim(invalidWorkspaceCharacter.ReplaceAllString(parts[0], "_"), "._-"), nil
}

func ManagerMemoryRoot(repoRoot string) (string, error) {
	return filepath.Abs(filepath.Join(repoRoot, ".UM-MEM", "memory"))
}

func WorkspaceMemoryRoot(repoRoot, workspaceID string) (string, error) {
	segment, err := NormalizeWorkspaceID(workspaceID)
	if err != nil {
		return "", err
	}
	if segment == "" {
		return "", errors.New("workspace_id is required for workspace_memory_root")
	}
	if segment == ManagerWorkspaceID {
		return ManagerMemoryRoot(repoRoot)
	}
	return filepath.Abs(filepath.Join(repoRoot, "workspaces", segment, ".memory"))
}

func WorkspaceRoot(repoRoot, workspaceID string) (string, error) {
	segment, err := NormalizeWorkspaceID(workspaceID)
	if err != nil {
		return "", err
	}
	if segment == "" {
		return "", errors.New("workspace_id is required for workspace_root")
	}
	return filepath.Abs(filepath.Join(repoRoot, "workspaces", segment))
}

func IsManagerWorkspace(workspaceID string) bool {
	segment, err := NormalizeWorkspaceID(workspaceID)
	return err == nil && segment == ManagerWorkspaceID
}

func PalacePathFor(repoRoot, workspaceID string) (string, error) {
	segment, err := NormalizeWorkspaceID(workspaceID)
	if err != nil {
		return "", err
	}
	if segment == "" {
		return filepath.Abs(filepath.Join(repoRoot, ".UM-MEM", "palace"))
	}
	root, err := WorkspaceMemoryRoot(repoRoot, segment)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "palace"), nil
}
