package hosthook

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCodexHookBuildsContentFreeHostState(t *testing.T) {
	cwd := t.TempDir()
	raw, err := json.Marshal(map[string]any{"hook_event_name": "UserPromptSubmit",
		"session_id": "session-1", "turn_id": "turn-1", "cwd": cwd,
		"prompt": "Private user text stays outside the event"})
	if err != nil {
		t.Fatal(err)
	}
	event, schedulerType, err := ParseCodex(raw, cwd)
	if err != nil || schedulerType != "prospective_trigger" {
		t.Fatalf("Codex hook failed: %+v, %s, %v", event, schedulerType, err)
	}
	payload, err := event.HostStatePayload(schedulerType)
	if err != nil || payload.EventID != "codex-host-state:1fdbd8507d7f861629773db21c0080bc3a6308447dca8309af78d2698ca80a38" {
		t.Fatalf("Codex hook state differs from Python: %+v, %v", payload, err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil || strings.Contains(string(encoded), "Private user text") {
		t.Fatalf("host state retained prompt: %s, %v", encoded, err)
	}
	if response := CodexControlResponse("UserPromptSubmit", "Advisory context", ""); response["continue"] != true || response["hookSpecificOutput"] == nil {
		t.Fatalf("Codex prompt control response changed: %+v", response)
	}
}

func TestCodexHookRejectsForgedCWDAndOversizedInput(t *testing.T) {
	cwd, foreign := t.TempDir(), t.TempDir()
	raw, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "session_id": "session-1",
		"turn_id": "turn-1", "cwd": foreign})
	if _, _, err := ParseCodex(raw, cwd); err == nil {
		t.Fatal("hook accepted a foreign working directory")
	}
	if _, _, err := ParseCodex(make([]byte, MaxHookInputBytes+1), cwd); err == nil {
		t.Fatal("hook accepted oversized input")
	}
	if _, _, err := ParseCodex([]byte(`{"hook_event_name":"Stop"} garbage`), cwd); err == nil {
		t.Fatal("hook accepted trailing JSON")
	}
}
