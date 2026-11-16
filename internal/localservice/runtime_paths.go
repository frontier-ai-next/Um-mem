package localservice

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/internal/paths"
	"golang.org/x/text/unicode/norm"
)

// RuntimePaths are the content-free locator files shared by host integrations.
// Their identity includes host namespaces so a workspace on another host never
// claims the listener in this process namespace.
type RuntimePaths struct {
	Registry    string
	Bootstrap   string
	StartupLock string
}

func localHostScope() ([]string, error) {
	if runtime.GOOS == "linux" {
		bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
		if err != nil {
			return nil, err
		}
		pid, err := os.Readlink("/proc/self/ns/pid")
		if err != nil {
			return nil, err
		}
		network, err := os.Readlink("/proc/self/ns/net")
		if err != nil {
			return nil, err
		}
		return []string{strings.TrimSpace(string(bootID)), pid, network}, nil
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	platform := runtime.GOOS
	if runtime.GOOS == "windows" {
		platform = "win32"
	}
	return []string{platform, hostname}, nil
}

func Paths(identity Identity) (RuntimePaths, error) {
	scope, err := localHostScope()
	if err != nil {
		return RuntimePaths{}, err
	}
	return runtimePathsForScope(identity, scope)
}

// SLOPath preserves the numeric-only telemetry file name used by the Python
// service, including the profile-specific registry stem and backend digest.
func SLOPath(identity Identity) (string, error) {
	runtimeFiles, err := Paths(identity)
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(norm.NFC.String(strings.TrimSpace(identity.RepoRoot)))
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	root = norm.NFC.String(filepath.Clean(root))
	workspace, err := paths.NormalizeWorkspaceID(identity.WorkspaceID)
	if err != nil || workspace == "" || identity.Backend == "" {
		return "", errors.New("service SLO identity is invalid")
	}
	material := root + "|" + workspace + "|" + identity.Backend
	digest := sha256.Sum256([]byte(material))
	stem := strings.TrimSuffix(filepath.Base(runtimeFiles.Registry), filepath.Ext(runtimeFiles.Registry))
	return filepath.Join(filepath.Dir(runtimeFiles.Registry), stem+"."+hex.EncodeToString(digest[:])[:20]+".slo.sqlite"), nil
}

func runtimePathsForScope(identity Identity, scope []string) (RuntimePaths, error) {
	root, err := normalizeRoot(identity.RepoRoot)
	if err != nil {
		return RuntimePaths{}, err
	}
	workspace, err := paths.NormalizeWorkspaceID(identity.WorkspaceID)
	if err != nil || workspace == "" || identity.ProfileID == "" || len(scope) == 0 {
		return RuntimePaths{}, errors.New("local service runtime identity is invalid")
	}
	material, err := jsoncompat.MarshalPython([]any{workspace, identity.ProfileID, scope}, true)
	if err != nil {
		return RuntimePaths{}, err
	}
	digest := sha256.Sum256(material)
	base := filepath.Join(root, ".UM-MEM", "memory", "local_service_"+hex.EncodeToString(digest[:])[:20])
	return RuntimePaths{Registry: base + ".json", Bootstrap: base + ".bootstrap.json",
		StartupLock: base + ".startup.lock"}, nil
}
