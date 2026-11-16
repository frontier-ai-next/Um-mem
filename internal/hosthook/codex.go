package hosthook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"
)

const MaxHookInputBytes = 1_048_576

type codexSpec struct {
	turnScoped    bool
	fields        []string
	subtaskField  string
	schedulerType string
}

var codexSpecs = map[string]codexSpec{
	"SessionStart":      {fields: []string{"source"}, schedulerType: "session_boundary"},
	"UserPromptSubmit":  {turnScoped: true, schedulerType: "prospective_trigger"},
	"PreToolUse":        {turnScoped: true, fields: []string{"tool_name", "tool_use_id"}, schedulerType: "prospective_trigger"},
	"PermissionRequest": {turnScoped: true, fields: []string{"tool_name"}, schedulerType: "high_risk_boundary"},
	"PostToolUse":       {turnScoped: true, fields: []string{"tool_name", "tool_use_id"}, schedulerType: "tool_result"},
	"PreCompact":        {turnScoped: true, fields: []string{"trigger"}, schedulerType: "compaction"},
	"PostCompact":       {turnScoped: true, fields: []string{"trigger"}, schedulerType: "compaction"},
	"SubagentStart":     {turnScoped: true, fields: []string{"agent_id", "agent_type"}, subtaskField: "agent_id", schedulerType: "subgoal_transition"},
	"SubagentStop":      {turnScoped: true, fields: []string{"agent_id", "agent_type"}, subtaskField: "agent_id", schedulerType: "subgoal_transition"},
	"Stop":              {turnScoped: true, schedulerType: "turn_completion"},
	"SessionEnd":        {fields: []string{"reason"}, schedulerType: "session_boundary"},
}

func parsePayload(raw []byte, host string) (map[string]any, error) {
	if len(raw) > MaxHookInputBytes {
		return nil, fmt.Errorf("%s hook input exceeds protocol limit", host)
	}
	if !utf8.Valid(raw) {
		return nil, fmt.Errorf("%s hook input is not UTF-8", host)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("%s hook input is not valid JSON", host)
	}
	if payload == nil {
		return nil, fmt.Errorf("%s hook input must be an object", host)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("%s hook input is not valid JSON", host)
	}
	return payload, nil
}

func identifier(payload map[string]any, field, host string, limit int) (string, error) {
	value, ok := payload[field].(string)
	if !ok {
		return "", fmt.Errorf("%s hook %s is required", host, field)
	}
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > limit || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("%s hook %s is required", host, field)
	}
	return value, nil
}

func matchingCWD(payload map[string]any, workingDir, host string) error {
	raw, ok := payload["cwd"].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return fmt.Errorf("%s hook cwd is required", host)
	}
	provided, err := filepath.Abs(raw)
	if err != nil {
		return fmt.Errorf("%s hook cwd is invalid", host)
	}
	provided, err = filepath.EvalSymlinks(provided)
	if err != nil {
		return fmt.Errorf("%s hook cwd is invalid", host)
	}
	current, err := filepath.Abs(workingDir)
	if err != nil {
		return fmt.Errorf("%s hook working directory is invalid", host)
	}
	current, err = filepath.EvalSymlinks(current)
	if err != nil {
		return fmt.Errorf("%s hook working directory is invalid", host)
	}
	provided, current = filepath.Clean(provided), filepath.Clean(current)
	if runtime.GOOS == "windows" {
		provided, current = strings.ToLower(provided), strings.ToLower(current)
	}
	if provided != current {
		return fmt.Errorf("%s hook cwd does not match process cwd", host)
	}
	return nil
}

func ParseCodex(raw []byte, workingDir string) (Event, string, error) {
	payload, err := parsePayload(raw, "codex")
	if err != nil {
		return Event{}, "", err
	}
	name, err := identifier(payload, "hook_event_name", "codex", 256)
	if err != nil {
		return Event{}, "", err
	}
	spec, ok := codexSpecs[name]
	if !ok {
		return Event{}, "", errors.New("unsupported Codex hook event")
	}
	session, err := identifier(payload, "session_id", "codex", 256)
	if err != nil {
		return Event{}, "", err
	}
	if err := matchingCWD(payload, workingDir, "codex"); err != nil {
		return Event{}, "", err
	}
	event := Event{HostSlug: "codex", HostLabel: "Codex", AdapterID: "codex_hooks",
		CapabilityCode: "codex_hook", SchemaVersion: 1, EventName: name, SessionID: session,
		IdentityFields: [][2]string{}}
	if spec.turnScoped {
		event.StageID, err = identifier(payload, "turn_id", "codex", 256)
		if err != nil {
			return Event{}, "", err
		}
		event.StageKind = "turn"
	}
	for _, field := range spec.fields {
		value, err := identifier(payload, field, "codex", 256)
		if err != nil {
			return Event{}, "", err
		}
		event.IdentityFields = append(event.IdentityFields, [2]string{field, value})
		if field == spec.subtaskField {
			event.SubtaskID, event.SubtaskKind = value, "subagent"
		}
	}
	return event, spec.schedulerType, nil
}

func CodexControlResponse(eventName, context, errorText string) map[string]any {
	response := map[string]any{}
	if map[string]bool{"SessionStart": true, "UserPromptSubmit": true,
		"PreCompact": true, "PostCompact": true, "SubagentStop": true,
		"Stop": true, "SessionEnd": true}[eventName] {
		response["continue"] = true
	}
	if context != "" && eventName == "UserPromptSubmit" {
		response["hookSpecificOutput"] = map[string]any{"hookEventName": eventName, "additionalContext": context}
	}
	if errorText != "" {
		if _, known := codexSpecs[eventName]; known {
			response["systemMessage"] = "UM-MEM hook: " + errorText
		}
	}
	return response
}
