package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hookInput(t *testing.T, event string) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"hook_event_name": event, "session_id": "session-1", "cwd": cwd}
	if event == "UserPromptSubmit" {
		payload["turn_id"] = "turn-1"
		payload["prompt"] = "Remember the project architecture"
	}
	if event == "Stop" {
		payload["stop_hook_active"] = false
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestCodexHookFailsOpenWhenServiceIsUnavailable(t *testing.T) {
	var output bytes.Buffer
	err := run([]string{"--host", "codex", "--repo-root", t.TempDir()},
		strings.NewReader(hookInput(t, "UserPromptSubmit")), &output)
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["continue"] != true || response["systemMessage"] !=
		"UM-MEM hook: local service context read failed" {
		t.Fatalf("unexpected Codex control response: %#v", response)
	}
	if _, exists := response["hookSpecificOutput"]; exists {
		t.Fatalf("unavailable memory must not inject prompt context: %#v", response)
	}
}

func TestCodexHookReportsMalformedInputWithoutBlocking(t *testing.T) {
	var output bytes.Buffer
	if err := run([]string{"--host", "codex"}, strings.NewReader(`{"hook_event_name":"Stop"}`), &output); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["continue"] != true || response["systemMessage"] == nil {
		t.Fatalf("missing identity must produce a nonblocking control response: %#v", response)
	}
}

func TestClaudeHookIsSilentWithoutReadyService(t *testing.T) {
	var output bytes.Buffer
	err := run([]string{"--host", "claude", "--repo-root", t.TempDir()},
		strings.NewReader(hookInput(t, "Stop")), &output)
	if err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("Claude Stop guidance needs an accepted capture, got %q", output.String())
	}
}

func TestHookIdentityUsesExplicitScope(t *testing.T) {
	t.Setenv("UM_MEM_WORKSPACE", "wrong-workspace")
	t.Setenv("UM_MEM_PROFILE", "wrong-profile")
	t.Setenv("UM_MEM_BACKEND", "hindsight")
	root := t.TempDir()
	identity, err := identityFor(options{repoRoot: root, workspace: "team", profile: "writer",
		backend: "zvec"}, root)
	if err != nil {
		t.Fatal(err)
	}
	if identity.RepoRoot != filepath.Clean(root) || identity.WorkspaceID != "team" ||
		identity.ProfileID != "writer" || identity.Backend != "zvec" {
		t.Fatalf("explicit scope lost: %#v", identity)
	}
}

func TestHookInstallAndStatusCommands(t *testing.T) {
	project := t.TempDir()
	for _, step := range []struct {
		flag      string
		installed bool
	}{{"--status", false}, {"--install", true}, {"--status", true}} {
		var output bytes.Buffer
		if err := run([]string{"--host", "codex", "--project-dir", project,
			"--repo-root", project, step.flag},
			strings.NewReader(""), &output); err != nil {
			t.Fatal(err)
		}
		var state struct {
			Installed bool `json:"installed"`
		}
		if err := json.Unmarshal(output.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		if state.Installed != step.installed {
			t.Fatalf("%s reported installed=%v", step.flag, state.Installed)
		}
	}
}

func TestHermesHookAndInstallUseGoBinaryWithoutPython(t *testing.T) {
	home := t.TempDir()
	var output bytes.Buffer
	if err := run([]string{"--host", "hermes", "--project-dir", home,
		"--repo-root", home, "--backend", "mnemosyne", "--install"},
		strings.NewReader(""), &output); err != nil {
		t.Fatal(err)
	}
	var state struct {
		Installed bool `json:"installed"`
	}
	if err := json.Unmarshal(output.Bytes(), &state); err != nil || !state.Installed {
		t.Fatalf("Hermes shell hook was not installed: %s %v", output.String(), err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"hook_event_name": "pre_llm_call",
		"session_id": "session-1", "cwd": cwd, "profile": "default",
		"extra": map[string]any{"turn_id": "turn-1", "user_message": "Recall project facts"}})
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run([]string{"--host", "hermes", "--repo-root", home,
		"--backend", "mnemosyne"}, bytes.NewReader(raw), &output); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(output.Bytes(), &response); err != nil || len(response) != 0 {
		t.Fatalf("unavailable service leaked Hermes prompt context: %s %v", output.String(), err)
	}
}
