package mcpsettings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/paths"
)

const ServerName = "um_mem"
const managedMarker = "um-memory"
const maxSettingsBytes = 4 << 20

type Identity struct {
	RepoRoot    string
	WorkspaceID string
	ProfileID   string
	Backend     string
}

func (identity Identity) normalize() (Identity, error) {
	if strings.TrimSpace(identity.RepoRoot) == "" ||
		identity.ProfileID == "" || identity.ProfileID != strings.TrimSpace(identity.ProfileID) ||
		identity.ProfileID == "." || identity.ProfileID == ".." ||
		strings.ContainsAny(identity.ProfileID, "/\\\x00\r\n") {
		return Identity{}, errors.New("MCP installation requires a safe repository root and profile")
	}
	root, err := filepath.Abs(identity.RepoRoot)
	if err != nil {
		return Identity{}, err
	}
	workspace, err := paths.NormalizeWorkspaceID(identity.WorkspaceID)
	if err != nil || workspace == "" {
		return Identity{}, errors.New("MCP installation workspace ID is invalid")
	}
	if identity.Backend != "mempalace" && identity.Backend != "hindsight" &&
		identity.Backend != "mnemosyne" && identity.Backend != "zvec" {
		return Identity{}, errors.New("MCP installation backend is invalid")
	}
	identity.RepoRoot, identity.WorkspaceID = filepath.Clean(root), workspace
	return identity, nil
}

type ServerConfig struct {
	Command string            `json:"command" yaml:"command"`
	Args    []string          `json:"args" yaml:"args"`
	Env     map[string]string `json:"env" yaml:"env"`
	Enabled bool              `json:"enabled" yaml:"enabled"`
}

func NewServerConfig(executable string, identity Identity) (ServerConfig, error) {
	identity, err := identity.normalize()
	if err != nil {
		return ServerConfig{}, err
	}
	command, err := filepath.Abs(executable)
	if err != nil {
		return ServerConfig{}, err
	}
	info, err := os.Stat(command)
	if err != nil || info.IsDir() {
		return ServerConfig{}, errors.New("um-memory-mcp executable was not found")
	}
	return ServerConfig{Command: command, Args: []string{"--repo-root", identity.RepoRoot,
		"--workspace", identity.WorkspaceID, "--profile", identity.ProfileID,
		"--backend", identity.Backend}, Env: map[string]string{
		"UM_MEM_MANAGED_CONFIG": managedMarker, "UM_MEM_REPO_ROOT": identity.RepoRoot,
		"UM_MEM_WORKSPACE": identity.WorkspaceID, "UM_MEM_PROFILE": identity.ProfileID,
		"UM_MEM_BACKEND": identity.Backend}, Enabled: true}, nil
}

func readSettings(path string) ([]byte, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxSettingsBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxSettingsBytes {
		return nil, errors.New("host settings exceed size limit")
	}
	return raw, nil
}

func writeSettings(path string, raw []byte) error {
	if len(raw) > maxSettingsBytes {
		return errors.New("host settings exceed size limit")
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(parent, ".um-memory-mcp-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func managedEntry(entry any, host string) error {
	if entry == nil {
		return nil
	}
	mapping, ok := entry.(map[string]any)
	if !ok {
		return fmt.Errorf("%s already has a non-object MCP server named %q", host, ServerName)
	}
	environment, ok := mapping["env"].(map[string]any)
	if !ok || environment["UM_MEM_MANAGED_CONFIG"] != managedMarker {
		return fmt.Errorf("%s already has an unmanaged MCP server named %q", host, ServerName)
	}
	return nil
}

func InstallClaude(path string, config ServerConfig) error {
	raw, err := readSettings(path)
	if err != nil {
		return err
	}
	settings := map[string]any{}
	if len(bytes.TrimSpace(raw)) != 0 {
		if err := json.Unmarshal(raw, &settings); err != nil || settings == nil {
			return fmt.Errorf("Claude Code settings are not valid JSON: %s", path)
		}
	}
	servers := map[string]any{}
	if existing, present := settings["mcpServers"]; present {
		var ok bool
		servers, ok = existing.(map[string]any)
		if !ok {
			return errors.New("Claude Code mcpServers setting must be an object")
		}
	}
	if err := managedEntry(servers[ServerName], "Claude Code"); err != nil {
		return err
	}
	servers[ServerName] = map[string]any{"command": config.Command,
		"args": config.Args, "env": config.Env}
	settings["mcpServers"] = servers
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return writeSettings(path, append(encoded, '\n'))
}
