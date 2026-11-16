package hosthook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/localservice"
)

func TestInstallHookSettingsPreservesUnrelatedAndReplacesLegacy(t *testing.T) {
	project := t.TempDir()
	path, err := settingsPath(project, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	initial := `{"model":"keep","hooks":{"UserPromptSubmit":[{"matcher":"keep","hooks":[` +
		`{"type":"command","command":"other-tool"},` +
		`{"type":"command","command":"python -m um_mem.integrations.codex_hooks"}]}],` +
		`"CustomEvent":[{"hooks":[{"type":"command","command":"custom-tool"}]}]}}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(project, "renamed-memory-helper")
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	identity := localservice.Identity{RepoRoot: project, WorkspaceID: "alpha", ProfileID: "team", Backend: "mnemosyne"}
	for i := 0; i < 2; i++ {
		if _, err := InstallHookSettings(project, "codex", executable, identity); err != nil {
			t.Fatal(err)
		}
	}
	settings, hooks, err := readHookSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if settings["model"] != "keep" || hooks["CustomEvent"] == nil {
		t.Fatalf("unrelated settings were lost: %#v", settings)
	}
	groups := hooks["UserPromptSubmit"].([]any)
	if len(groups) != 2 {
		t.Fatalf("expected one retained group and one Go group: %#v", groups)
	}
	retained := groups[0].(map[string]any)
	if retained["matcher"] != "keep" || len(retained["hooks"].([]any)) != 1 {
		t.Fatalf("non-UM-MEM handler was changed: %#v", retained)
	}
	for _, rawGroup := range groups {
		for _, rawHandler := range rawGroup.(map[string]any)["hooks"].([]any) {
			if strings.Contains(rawHandler.(map[string]any)["command"].(string), "um_mem.integrations.codex_hooks") {
				t.Fatal("legacy Python hook remained active")
			}
		}
	}
	state, err := HookSettingsState(project, "codex")
	if err != nil || !state.Installed || len(state.InstalledEvents) != len(codexRegistrations) {
		t.Fatalf("unexpected installation status: %#v, %v", state, err)
	}
}

func TestClaudeHookSettingsCoverageAndMalformedSettings(t *testing.T) {
	project := t.TempDir()
	executable := filepath.Join(project, "renamed-memory-helper")
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	identity := localservice.Identity{RepoRoot: project, WorkspaceID: "alpha", ProfileID: "team", Backend: "mnemosyne"}
	before, err := HookSettingsState(project, "claude")
	if err != nil || before.Installed || len(before.MissingEvents) != len(claudeRegistrations) {
		t.Fatalf("unexpected preinstall status: %#v, %v", before, err)
	}
	path, err := InstallHookSettings(project, "claude", executable, identity)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(content, &settings); err != nil {
		t.Fatal(err)
	}
	hooks := settings["hooks"].(map[string]any)
	if _, unsafe := hooks["WorktreeCreate"]; unsafe {
		t.Fatal("Claude WorktreeCreate hook can replace host defaults")
	}
	if state, err := HookSettingsState(project, "claude"); err != nil || !state.Installed {
		t.Fatalf("unexpected Claude installation status: %#v, %v", state, err)
	}
	if err := os.WriteFile(path, []byte(`{"hooks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHookSettings(project, "claude", executable, identity); err == nil {
		t.Fatal("malformed settings must not be overwritten")
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != `{"hooks":[]}` {
		t.Fatalf("invalid original settings were altered: %q, %v", unchanged, err)
	}
}

func TestInstalledHookPinsExplicitServiceIdentity(t *testing.T) {
	project := t.TempDir()
	executable := filepath.Join(project, "memory-hook")
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(project, "memory store")
	identity := localservice.Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team", Backend: "mnemosyne"}
	if _, err := InstallHookSettings(project, "codex", executable, identity); err != nil {
		t.Fatal(err)
	}
	_, hooks, err := readHookSettings(filepath.Join(project, ".codex", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	group := hooks["UserPromptSubmit"].([]any)[0].(map[string]any)
	handler := group["hooks"].([]any)[0].(map[string]any)
	for _, field := range []string{"command", "commandWindows"} {
		command := handler[field].(string)
		for _, value := range []string{"--repo-root", root, "--workspace", "alpha", "--profile", "team", "--backend", "mnemosyne"} {
			if !strings.Contains(command, value) {
				t.Fatalf("%s omitted pinned %q: %s", field, value, command)
			}
		}
	}
	identity.ProfileID = `team" --backend hindsight`
	if _, err := InstallHookSettings(project, "codex", executable, identity); err == nil {
		t.Fatal("unsafe profile was accepted in a host command")
	}
}
