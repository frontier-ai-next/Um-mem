package localservice

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/paths"
)

// SchedulerPath names the role-independent state file used by the Python
// listener. A selected backend or profile gets its own durable queue.
func SchedulerPath(identity Identity) (string, string, error) {
	root, err := normalizeRoot(identity.RepoRoot)
	if err != nil {
		return "", "", err
	}
	workspace, err := paths.NormalizeWorkspaceID(identity.WorkspaceID)
	if err != nil || workspace == "" || identity.ProfileID == "" || identity.Backend == "" {
		return "", "", errors.New("local service scheduler identity is invalid")
	}
	identity.RepoRoot, identity.WorkspaceID = root, workspace
	runtimePaths, err := Paths(identity)
	if err != nil {
		return "", "", err
	}
	// Python normalizes the identity path for comparison, then resolves it
	// again while hashing. On Windows that final resolution restores the
	// filesystem's spelling of each component.
	hashRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	encoded, err := jsoncompat.MarshalPython(map[string]any{
		"repo_root": hashRoot, "workspace_id": workspace,
		"profile_id": identity.ProfileID, "backend": identity.Backend,
		"protocol_version": ProtocolVersion,
	}, false)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(encoded)
	digest := hex.EncodeToString(sum[:])
	stem := strings.TrimSuffix(filepath.Base(runtimePaths.Registry), filepath.Ext(runtimePaths.Registry))
	return filepath.Join(filepath.Dir(runtimePaths.Registry), stem+"."+digest[:20]+".scheduler.sqlite"), digest, nil
}
