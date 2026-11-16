package localservice

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type bootstrapFile struct {
	SchemaVersion int               `json:"schema_version"`
	WorkspaceID   string            `json:"workspace_id"`
	ProfileID     string            `json:"profile_id"`
	Backend       string            `json:"backend"`
	Secrets       map[string]string `json:"secrets"`
}

type registryFile struct {
	SchemaVersion   int    `json:"schema_version"`
	APIVersion      int    `json:"api_version"`
	ProtocolVersion int    `json:"protocol_version"`
	Host            string `json:"host"`
	Port            int    `json:"port"`
	PID             int    `json:"pid"`
	OwnerPID        int    `json:"owner_pid,omitempty"`
	WorkspaceID     string `json:"workspace_id"`
	ProfileID       string `json:"profile_id"`
	Backend         string `json:"backend"`
	Transport       string `json:"transport"`
}

func roleNames() ([]string, error) {
	var capabilities map[string][]string
	if err := json.Unmarshal(roleCapabilitiesJSON, &capabilities); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(capabilities))
	for role := range capabilities {
		names = append(names, role)
	}
	return names, nil
}

func validBootstrap(file bootstrapFile, identity Identity, roles []string) bool {
	if file.SchemaVersion != SchemaVersion || file.WorkspaceID != identity.WorkspaceID ||
		file.ProfileID != identity.ProfileID || file.Backend != identity.Backend ||
		len(file.Secrets) != len(roles) {
		return false
	}
	for _, role := range roles {
		if file.Secrets[role] == "" {
			return false
		}
	}
	return true
}

// LoadOrCreateBootstrap reuses Python's role credentials when present. A
// malformed existing file fails closed and is never silently replaced.
func LoadOrCreateBootstrap(identity Identity) (map[string]string, error) {
	paths, err := Paths(identity)
	if err != nil {
		return nil, err
	}
	roles, err := roleNames()
	if err != nil {
		return nil, err
	}
	read := func() (map[string]string, error) {
		data, err := os.ReadFile(paths.Bootstrap)
		if err != nil {
			return nil, err
		}
		var file bootstrapFile
		if json.Unmarshal(data, &file) != nil || !validBootstrap(file, identity, roles) {
			return nil, errors.New("local service bootstrap identity is invalid")
		}
		return file.Secrets, nil
	}
	secrets, err := read()
	if err == nil {
		return secrets, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(paths.Bootstrap), 0o700); err != nil {
		return nil, err
	}
	secrets = make(map[string]string, len(roles))
	for _, role := range roles {
		random := make([]byte, 32)
		if _, err := rand.Read(random); err != nil {
			return nil, err
		}
		secrets[role] = base64.RawURLEncoding.EncodeToString(random)
	}
	data, err := json.Marshal(bootstrapFile{SchemaVersion: SchemaVersion,
		WorkspaceID: identity.WorkspaceID, ProfileID: identity.ProfileID,
		Backend: identity.Backend, Secrets: secrets})
	if err != nil {
		return nil, err
	}
	// O_EXCL avoids replacing credentials created by a competing process. Any
	// reader of the new file retries through the same validation path.
	file, err := os.OpenFile(paths.Bootstrap, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return read()
	}
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return secrets, nil
}

func writeRegistry(identity Identity, port, ownerPID int) (RuntimePaths, error) {
	if port < 1 || port > 65535 {
		return RuntimePaths{}, errors.New("local service registry port is invalid")
	}
	paths, err := Paths(identity)
	if err != nil {
		return RuntimePaths{}, err
	}
	if err := os.MkdirAll(filepath.Dir(paths.Registry), 0o700); err != nil {
		return RuntimePaths{}, err
	}
	if _, err := os.Stat(paths.Registry); err == nil {
		return RuntimePaths{}, errors.New("local service registry already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return RuntimePaths{}, err
	}
	registry := registryFile{SchemaVersion: SchemaVersion, APIVersion: APIVersion,
		ProtocolVersion: ProtocolVersion, Host: "127.0.0.1", Port: port,
		PID: os.Getpid(), OwnerPID: ownerPID, WorkspaceID: identity.WorkspaceID,
		ProfileID: identity.ProfileID, Backend: identity.Backend, Transport: "loopback_http"}
	data, err := json.Marshal(registry)
	if err != nil {
		return RuntimePaths{}, err
	}
	file, err := os.CreateTemp(filepath.Dir(paths.Registry), ".local-service-registry-*")
	if err != nil {
		return RuntimePaths{}, err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return RuntimePaths{}, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return RuntimePaths{}, err
	}
	if err := file.Close(); err != nil {
		return RuntimePaths{}, err
	}
	// A hard link publishes the fully synced file only if no peer has already
	// registered this identity. Rename would replace a peer on POSIX.
	if err := os.Link(file.Name(), paths.Registry); err != nil {
		return RuntimePaths{}, fmt.Errorf("publish local service registry: %w", err)
	}
	return paths, nil
}

func clearRegistry(paths RuntimePaths, pid, port int) error {
	data, err := os.ReadFile(paths.Registry)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var registry registryFile
	if err := json.Unmarshal(data, &registry); err != nil {
		return err
	}
	if registry.PID != pid || registry.Port != port {
		return nil
	}
	return os.Remove(paths.Registry)
}
