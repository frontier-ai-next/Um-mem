package hosthook

import (
	"encoding/json"
	"testing"
)

func TestClaudeSubagentHookMatchesPython(t *testing.T) {
	cwd := t.TempDir()
	raw, err := json.Marshal(map[string]any{"hook_event_name": "SubagentStart",
		"session_id": "session-1", "cwd": cwd, "agent_id": "agent-1", "agent_type": "Explore",
		"prompt": "Private subagent prompt"})
	if err != nil {
		t.Fatal(err)
	}
	event, kind, err := ParseClaude(raw, cwd, nil)
	if err != nil || kind != "subgoal_transition" || event.SubtaskID != "agent-1" {
		t.Fatalf("Claude event parse failed: %+v, %s, %v", event, kind, err)
	}
	digest, err := event.Digest()
	if err != nil || digest != "6c60f59a324a40cfda45ff590c0daf4ec448e0fbdb5c49c6e6064749bf27eb61" {
		t.Fatalf("Claude event digest differs from Python: %s, %v", digest, err)
	}
	payload, err := event.HostStatePayload(kind)
	if err != nil || payload.StageID != "claude-session-boundary" ||
		payload.EventID != "claude-host-state:"+digest {
		t.Fatalf("Claude host-state payload differs: %+v, %v", payload, err)
	}
}

func TestClaudeUnstableHookUsesNonceAndBatchIsBounded(t *testing.T) {
	cwd := t.TempDir()
	raw, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "session_id": "session-1", "cwd": cwd})
	one, _, err := ParseClaude(raw, cwd, func() (string, error) { return "nonce-1", nil })
	if err != nil {
		t.Fatal(err)
	}
	two, _, err := ParseClaude(raw, cwd, func() (string, error) { return "nonce-2", nil })
	if err != nil {
		t.Fatal(err)
	}
	a, _ := one.Digest()
	b, _ := two.Digest()
	if a == b {
		t.Fatal("separate Stop captures reused one event identity")
	}
	raw, _ = json.Marshal(map[string]any{"hook_event_name": "PostToolBatch", "session_id": "session-1", "cwd": cwd, "tool_calls": []any{}})
	if _, _, err := ParseClaude(raw, cwd, nil); err == nil {
		t.Fatal("empty batch accepted")
	}
}
