package localservice

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/paths"
)

// WorkQueuePath names the Python-compatible durable background queue. The
// path is role-independent and isolated by repository, workspace, profile,
// and selected backend.
func WorkQueuePath(identity Identity) (string, error) {
	root, err := normalizeRoot(identity.RepoRoot)
	if err != nil {
		return "", err
	}
	workspace, err := paths.NormalizeWorkspaceID(identity.WorkspaceID)
	if err != nil || workspace == "" || identity.ProfileID == "" || identity.Backend == "" {
		return "", errors.New("local service work queue identity is invalid")
	}
	identity.RepoRoot, identity.WorkspaceID = root, workspace
	runtimePaths, err := Paths(identity)
	if err != nil {
		return "", err
	}
	hashRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	material := strings.Join([]string{hashRoot, workspace, identity.ProfileID, identity.Backend}, "|")
	sum := sha256.Sum256([]byte(material))
	digest := hex.EncodeToString(sum[:])[:20]
	stem := strings.TrimSuffix(filepath.Base(runtimePaths.Registry), filepath.Ext(runtimePaths.Registry))
	return filepath.Join(filepath.Dir(runtimePaths.Registry), stem+"."+digest+".work.sqlite"), nil
}
