package mcpsettings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

func tomlString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func managedTableHeader(line string) bool {
	header := strings.TrimSpace(line)
	if !strings.HasPrefix(header, "[") || strings.HasPrefix(header, "[[") {
		return false
	}
	end := strings.IndexByte(header, ']')
	if end < 0 {
		return false
	}
	key := header[1:end]
	return key == "mcp_servers.um_mem" || strings.HasPrefix(key, "mcp_servers.um_mem.")
}

func isTOMLHeader(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "[") && strings.Contains(trimmed, "]")
}

func removeManagedTables(raw []byte) ([]byte, bool) {
	var output bytes.Buffer
	managed, found := false, false
	for _, line := range strings.SplitAfter(string(raw), "\n") {
		if isTOMLHeader(line) {
			managed = managedTableHeader(line)
			found = found || managed
		}
		if !managed {
			output.WriteString(line)
		}
	}
	return output.Bytes(), found
}

// InstallCodex keeps every unrelated TOML table and comment byte-for-byte.
// Existing managed entries must use explicit tables so they can be replaced
// without rewriting user-owned settings through a lossy serializer.
func InstallCodex(path string, config ServerConfig) error {
	raw, err := readSettings(path)
	if err != nil {
		return err
	}
	parsed := map[string]any{}
	if len(bytes.TrimSpace(raw)) != 0 {
		if err := toml.Unmarshal(raw, &parsed); err != nil {
			return fmt.Errorf("Codex settings are not valid TOML: %w", err)
		}
	}
	servers := map[string]any{}
	if existing, present := parsed["mcp_servers"]; present {
		var ok bool
		servers, ok = existing.(map[string]any)
		if !ok {
			return errors.New("Codex mcp_servers setting must be a TOML table")
		}
	}
	entry := servers[ServerName]
	if err := managedEntry(entry, "Codex"); err != nil {
		return err
	}
	preserved, found := removeManagedTables(raw)
	if entry != nil && !found {
		return errors.New("Codex managed MCP entry is not an explicit table")
	}
	var output bytes.Buffer
	output.Write(bytes.TrimRight(preserved, "\r\n"))
	if output.Len() != 0 {
		output.WriteString("\n\n")
	}
	output.WriteString("[mcp_servers.um_mem]\n")
	fmt.Fprintf(&output, "command = %s\n", tomlString(config.Command))
	output.WriteString("args = [")
	for index, arg := range config.Args {
		if index != 0 {
			output.WriteString(", ")
		}
		output.WriteString(tomlString(arg))
	}
	output.WriteString("]\nenabled = true\nstartup_timeout_sec = 20\n")
	output.WriteString("tool_timeout_sec = 120\ndefault_tools_approval_mode = \"prompt\"\n")
	output.WriteString("\n[mcp_servers.um_mem.env]\n")
	for _, key := range []string{"UM_MEM_MANAGED_CONFIG", "UM_MEM_REPO_ROOT",
		"UM_MEM_WORKSPACE", "UM_MEM_PROFILE", "UM_MEM_BACKEND"} {
		fmt.Fprintf(&output, "%s = %s\n", key, tomlString(config.Env[key]))
	}
	var check map[string]any
	if err := toml.Unmarshal(output.Bytes(), &check); err != nil {
		return fmt.Errorf("Codex MCP settings update would be invalid: %w", err)
	}
	return writeSettings(path, output.Bytes())
}
