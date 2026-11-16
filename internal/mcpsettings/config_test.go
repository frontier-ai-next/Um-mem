package mcpsettings

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
	"gopkg.in/yaml.v3"
)

func testConfig(t *testing.T) ServerConfig {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config, err := NewServerConfig(executable, Identity{RepoRoot: filepath.Join(t.TempDir(), "store"),
		WorkspaceID: "alpha", ProfileID: "team", Backend: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func TestClaudeMCPInstallPreservesOtherServersAndRefusesUnmanagedName(t *testing.T) {
	config := testConfig(t)
	path := filepath.Join(t.TempDir(), ".claude.json")
	if err := os.WriteFile(path, []byte(`{"otherSetting":true,"mcpServers":{"docs":{"command":"docs"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallClaude(path, config); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(first, &settings); err != nil {
		t.Fatal(err)
	}
	servers := settings["mcpServers"].(map[string]any)
	managed := servers[ServerName].(map[string]any)
	if settings["otherSetting"] != true || servers["docs"].(map[string]any)["command"] != "docs" ||
		managed["command"] != config.Command || managed["args"].([]any)[0] != "--repo-root" {
		t.Fatalf("Claude MCP install changed unrelated settings: %+v", settings)
	}
	if err := InstallClaude(path, config); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatal("Claude MCP install changed on an exact replay")
	}
	if status := Status("claude", path); status["configured"] != true ||
		status["integration_mode"] != "mcp_tools_only" {
		t.Fatalf("Claude MCP status: %+v", status)
	}
	unmanaged := filepath.Join(t.TempDir(), "unmanaged.json")
	if err := os.WriteFile(unmanaged, []byte(`{"mcpServers":{"um_mem":{"command":"other"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallClaude(unmanaged, config); err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("Claude unmanaged name was overwritten: %v", err)
	}
}

func TestCodexMCPInstallPreservesCommentsAndRejectsUnmanagedEntry(t *testing.T) {
	config := testConfig(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "# keep this comment\nmodel = 'test'\n\n[mcp_servers.docs]\ncommand = 'docs'\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallCodex(path, config); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(first), original) {
		t.Fatalf("Codex original settings/comment changed: %s", first)
	}
	var parsed map[string]any
	if err := toml.Unmarshal(first, &parsed); err != nil {
		t.Fatal(err)
	}
	entry := parsed["mcp_servers"].(map[string]any)[ServerName].(map[string]any)
	if entry["default_tools_approval_mode"] != "prompt" || entry["command"] != config.Command ||
		entry["env"].(map[string]any)["UM_MEM_WORKSPACE"] != "alpha" {
		t.Fatalf("Codex MCP entry contract: %+v", entry)
	}
	if err := InstallCodex(path, config); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatalf("Codex MCP install was not idempotent:\n%s", second)
	}
	if status := Status("codex", path); status["configured"] != true {
		t.Fatalf("Codex MCP status: %+v", status)
	}
	unmanaged := filepath.Join(t.TempDir(), "unmanaged.toml")
	if err := os.WriteFile(unmanaged, []byte("[mcp_servers.um_mem]\ncommand = 'other'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallCodex(unmanaged, config); err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("Codex unmanaged name was overwritten: %v", err)
	}
}

func TestHermesMCPInstallPreservesOtherSettingsAndManagedExtras(t *testing.T) {
	config := testConfig(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := "# keep this comment\nmodel:\n  provider: test\nmcp_servers:\n  docs:\n    command: docs\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallHermes(path, config); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(first, &parsed); err != nil {
		t.Fatal(err)
	}
	servers := parsed["mcp_servers"].(map[string]any)
	entry := servers[ServerName].(map[string]any)
	if !bytes.Contains(first, []byte("# keep this comment")) ||
		parsed["model"].(map[string]any)["provider"] != "test" ||
		servers["docs"].(map[string]any)["command"] != "docs" ||
		entry["timeout"] != 120 || entry["command"] != config.Command {
		t.Fatalf("Hermes MCP install changed unrelated settings: %+v", parsed)
	}
	if err := InstallHermes(path, config); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatalf("Hermes MCP install was not idempotent:\n%s", second)
	}
	if status := Status("hermes", path); status["configured"] != true {
		t.Fatalf("Hermes MCP status: %+v", status)
	}
	unmanaged := filepath.Join(t.TempDir(), "unmanaged.yaml")
	if err := os.WriteFile(unmanaged, []byte("mcp_servers:\n  um_mem:\n    command: other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallHermes(unmanaged, config); err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("Hermes unmanaged name was overwritten: %v", err)
	}
}

func TestMCPStatusRejectsTamperedOrDisabledRegistration(t *testing.T) {
	config := testConfig(t)
	path := filepath.Join(t.TempDir(), "claude.json")
	if err := InstallClaude(path, config); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	entry := settings["mcpServers"].(map[string]any)[ServerName].(map[string]any)
	entry["args"].([]any)[3] = "foreign-workspace"
	write := func() {
		t.Helper()
		encoded, err := json.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	if status := Status("claude", path); status["configured"] != false {
		t.Fatalf("tampered identity was reported configured: %+v", status)
	}
	entry["args"].([]any)[3] = "alpha"
	entry["enabled"] = false
	write()
	if status := Status("claude", path); status["configured"] != false {
		t.Fatalf("disabled server was reported configured: %+v", status)
	}
	settings["mcpServers"] = []any{}
	write()
	if status := Status("claude", path); status["configuration_error"] == nil {
		t.Fatalf("malformed host settings lacked an error: %+v", status)
	}
}
