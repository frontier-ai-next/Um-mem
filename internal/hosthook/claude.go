package hosthook

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
)

type claudeSpec struct {
	schedulerType string
	fields        []string
	stageField    string
	stageKind     string
	subtaskField  string
	subtaskKind   string
	replayStable  bool
}

var claudeSpecs = map[string]claudeSpec{
	"SessionStart":       {schedulerType: "session_boundary", fields: []string{"source"}, replayStable: true},
	"UserPromptSubmit":   {schedulerType: "prospective_trigger"},
	"PreToolUse":         {schedulerType: "prospective_trigger", fields: []string{"tool_name", "tool_use_id"}, stageField: "tool_use_id", stageKind: "tool", replayStable: true},
	"PostToolUse":        {schedulerType: "tool_result", fields: []string{"tool_name", "tool_use_id"}, stageField: "tool_use_id", stageKind: "tool", replayStable: true},
	"PostToolUseFailure": {schedulerType: "tool_result", fields: []string{"tool_name", "tool_use_id"}, stageField: "tool_use_id", stageKind: "tool", replayStable: true},
	"PostToolBatch":      {schedulerType: "tool_result", replayStable: true},
	"SubagentStart":      {schedulerType: "subgoal_transition", fields: []string{"agent_id", "agent_type"}, subtaskField: "agent_id", subtaskKind: "subagent", replayStable: true},
	"SubagentStop":       {schedulerType: "subgoal_transition", fields: []string{"agent_id", "agent_type"}, subtaskField: "agent_id", subtaskKind: "subagent", replayStable: true},
	"TaskCreated":        {schedulerType: "subgoal_transition", fields: []string{"task_id"}, stageField: "task_id", stageKind: "task", replayStable: true},
	"TaskCompleted":      {schedulerType: "subgoal_transition", fields: []string{"task_id"}, stageField: "task_id", stageKind: "task", replayStable: true},
	"Stop":               {schedulerType: "turn_completion"},
	"StopFailure":        {schedulerType: "turn_completion", fields: []string{"error"}},
	"FileChanged":        {schedulerType: "source_change", fields: []string{"file_path", "event"}},
	"WorktreeCreate":     {schedulerType: "source_change", fields: []string{"name"}, replayStable: true},
	"WorktreeRemove":     {schedulerType: "source_change", fields: []string{"worktree_path"}, replayStable: true},
	"PreCompact":         {schedulerType: "compaction", fields: []string{"trigger"}},
	"PostCompact":        {schedulerType: "compaction", fields: []string{"trigger"}},
	"SessionEnd":         {schedulerType: "session_boundary", fields: []string{"reason"}, replayStable: true},
}

func randomNonce() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// ParseClaude retains only typed identity fields. The nonce supplier is
// injectable so fixture tests can replay an otherwise unstable event.
func ParseClaude(raw []byte, workingDir string, nonce func() (string, error)) (Event, string, error) {
	payload, err := parsePayload(raw, "Claude Code")
	if err != nil {
		return Event{}, "", err
	}
	name, err := identifier(payload, "hook_event_name", "Claude Code", 4096)
	if err != nil {
		return Event{}, "", err
	}
	spec, ok := claudeSpecs[name]
	if !ok {
		return Event{}, "", errors.New("unsupported Claude Code hook event")
	}
	session, err := identifier(payload, "session_id", "Claude Code", 4096)
	if err != nil {
		return Event{}, "", err
	}
	if err := matchingCWD(payload, workingDir, "Claude Code"); err != nil {
		return Event{}, "", err
	}
	event := Event{HostSlug: "claude", HostLabel: "Claude Code", AdapterID: "claude_hooks",
		CapabilityCode: "claude_code_hook", SchemaVersion: 1,
		EventName: name, SessionID: session, IdentityFields: [][2]string{},
		StageKind: spec.stageKind, SubtaskKind: spec.subtaskKind}
	if name == "PostToolBatch" {
		calls, ok := payload["tool_calls"].([]any)
		if !ok || len(calls) == 0 || len(calls) > 128 {
			return Event{}, "", errors.New("Claude Code hook tool_calls must be a bounded non-empty list")
		}
		for i, rawCall := range calls {
			call, ok := rawCall.(map[string]any)
			if !ok {
				return Event{}, "", errors.New("Claude Code hook tool_calls entries must be objects")
			}
			name, err := identifier(call, "tool_name", "Claude Code", 4096)
			if err != nil {
				return Event{}, "", err
			}
			id, err := identifier(call, "tool_use_id", "Claude Code", 4096)
			if err != nil {
				return Event{}, "", err
			}
			event.IdentityFields = append(event.IdentityFields, [2]string{fmt.Sprintf("tool_%d_name", i), name},
				[2]string{fmt.Sprintf("tool_%d_id", i), id})
		}
	} else {
		for _, field := range spec.fields {
			value, err := identifier(payload, field, "Claude Code", 4096)
			if err != nil {
				return Event{}, "", err
			}
			event.IdentityFields = append(event.IdentityFields, [2]string{field, value})
			if field == spec.stageField {
				event.StageID = value
			}
			if field == spec.subtaskField {
				event.SubtaskID = value
			}
		}
	}
	if !spec.replayStable {
		if nonce == nil {
			nonce = randomNonce
		}
		value, err := nonce()
		if err != nil {
			return Event{}, "", err
		}
		event.IdentityFields = append(event.IdentityFields, [2]string{"capture_nonce", value})
	}
	return event, spec.schedulerType, nil
}
