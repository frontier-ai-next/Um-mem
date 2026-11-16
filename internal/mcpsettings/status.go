package mcpsettings

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"

	toml "github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

func Install(host, path string, config ServerConfig) error {
	switch host {
	case "claude":
		return InstallClaude(path, config)
	case "codex":
		return InstallCodex(path, config)
	case "hermes":
		return InstallHermes(path, config)
	default:
		return errors.New("MCP host must be claude, codex or hermes")
	}
}

func Status(host, path string) map[string]any {
	result := map[string]any{"configured": false, "path": path,
		"integration_mode": "mcp_tools_only", "active_mask_delivery": false,
		"host_connection_tested": false, "server_name": "", "identity": map[string]string{}}
	raw, err := readSettings(path)
	if err != nil {
		result["configuration_error"] = err.Error()
		return result
	}
	var document map[string]any
	switch host {
	case "claude":
		if len(bytes.TrimSpace(raw)) != 0 {
			err = json.Unmarshal(raw, &document)
		}
	case "codex":
		if len(bytes.TrimSpace(raw)) != 0 {
			err = toml.Unmarshal(raw, &document)
		}
	case "hermes":
		if len(bytes.TrimSpace(raw)) != 0 {
			err = yaml.Unmarshal(raw, &document)
		}
	default:
		err = errors.New("MCP host must be claude, codex or hermes")
	}
	if err != nil {
		result["configuration_error"] = err.Error()
		return result
	}
	key := "mcp_servers"
	if host == "claude" {
		key = "mcpServers"
	}
	servers := map[string]any{}
	if rawServers, present := document[key]; present {
		var ok bool
		servers, ok = rawServers.(map[string]any)
		if !ok {
			result["configuration_error"] = "MCP server settings must be a mapping"
			return result
		}
	}
	entry, _ := servers[ServerName].(map[string]any)
	environment, _ := entry["env"].(map[string]any)
	command, _ := entry["command"].(string)
	arguments, _ := entry["args"].([]any)
	validArgs := len(arguments) == 8 && arguments[0] == "--repo-root" &&
		arguments[2] == "--workspace" && arguments[4] == "--profile" &&
		arguments[6] == "--backend"
	identityKeys := []string{"UM_MEM_REPO_ROOT", "UM_MEM_WORKSPACE", "UM_MEM_PROFILE", "UM_MEM_BACKEND"}
	if validArgs {
		for index, key := range identityKeys {
			value, ok := arguments[index*2+1].(string)
			if !ok || value == "" || environment[key] != value {
				validArgs = false
			}
		}
	}
	if validArgs {
		_, err = (Identity{RepoRoot: arguments[1].(string), WorkspaceID: arguments[3].(string),
			ProfileID: arguments[5].(string), Backend: arguments[7].(string)}).normalize()
		validArgs = err == nil
	}
	info, fileErr := os.Stat(command)
	enabled := entry["enabled"] != false
	configured := environment["UM_MEM_MANAGED_CONFIG"] == managedMarker &&
		fileErr == nil && !info.IsDir() && validArgs && enabled
	if configured {
		result["configured"] = true
		result["server_name"] = ServerName
		result["identity"] = map[string]any{"repo_root": environment["UM_MEM_REPO_ROOT"],
			"workspace": environment["UM_MEM_WORKSPACE"],
			"profile":   environment["UM_MEM_PROFILE"], "backend": environment["UM_MEM_BACKEND"]}
	}
	return result
}
