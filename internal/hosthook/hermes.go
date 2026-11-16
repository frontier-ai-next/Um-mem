package hosthook

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/localservice"
)

var hermesEventKinds = map[string]string{
	"on_session_start":    "session_boundary",
	"pre_llm_call":        "prospective_trigger",
	"post_llm_call":       "turn_completion",
	"pre_tool_call":       "prospective_trigger",
	"post_tool_call":      "tool_result",
	"post_compaction":     "compaction",
	"subagent_stop":       "subgoal_transition",
	"on_session_finalize": "session_boundary",
}

// ParseHermes accepts the documented shell-hook envelope. The event identity
// contains opaque host IDs and counts, never raw prompt or conversation text.
func ParseHermes(raw []byte, workingDir string) (Event, string, string, error) {
	payload, err := parsePayload(raw, "hermes")
	if err != nil {
		return Event{}, "", "", err
	}
	name, err := identifier(payload, "hook_event_name", "hermes", 256)
	if err != nil {
		return Event{}, "", "", err
	}
	kind, supported := hermesEventKinds[name]
	if !supported {
		return Event{}, "", "", errors.New("unsupported Hermes shell hook event")
	}
	session, err := identifier(payload, "session_id", "hermes", 256)
	if err != nil {
		return Event{}, "", "", err
	}
	if err := matchingCWD(payload, workingDir, "hermes"); err != nil {
		return Event{}, "", "", err
	}
	extra, _ := payload["extra"].(map[string]any)
	profile, _ := payload["profile"].(string)
	if len(profile) > 256 || strings.ContainsRune(profile, 0) {
		return Event{}, "", "", errors.New("Hermes profile is invalid")
	}
	event := Event{HostSlug: "hermes", HostLabel: "Hermes", AdapterID: "hermes_shell_hook",
		CapabilityCode: "hermes_shell_hook", SchemaVersion: 1, EventName: name,
		SessionID: session, IdentityFields: [][2]string{{"profile", profile}}}
	message, _ := extra["user_message"].(string)
	if len(message) > MaxHookInputBytes {
		return Event{}, "", "", errors.New("Hermes user message exceeds hook limit")
	}
	if name != "on_session_start" && name != "on_session_finalize" {
		stage := ""
		for _, key := range []string{"turn_id", "api_request_id", "task_id", "tool_call_id"} {
			if value, ok := extra[key].(string); ok && strings.TrimSpace(value) != "" && len(value) <= 256 {
				stage = value
				break
			}
		}
		if stage == "" {
			history, _ := extra["conversation_history"].([]any)
			stage = sha(fmt.Sprintf("%s\x00%s\x00%d\x00%s", session, name, len(history), message))[:32]
		}
		event.StageKind, event.StageID = "turn", stage
	}
	if tool, ok := payload["tool_name"].(string); ok && tool != "" && len(tool) <= 256 {
		event.IdentityFields = append(event.IdentityFields, [2]string{"tool_name", tool})
	}
	if callID, ok := extra["tool_call_id"].(string); ok && callID != "" && len(callID) <= 256 {
		event.IdentityFields = append(event.IdentityFields, [2]string{"tool_call_id", callID})
	}
	return event, kind, message, nil
}

// ReadHermesPromptContext uses the same bounded advisory path as Codex. The
// shell hook can return its result as {"context": ...} to Hermes.
func ReadHermesPromptContext(ctx context.Context, message string, event Event,
	workingDir string, identity localservice.Identity, client ContextCaller) (string, error) {
	if event.EventName != "pre_llm_call" {
		return "", nil
	}
	synthetic := event
	synthetic.EventName = "UserPromptSubmit"
	return ReadCodexPromptContext(ctx, map[string]any{"prompt": message}, synthetic,
		workingDir, identity, client)
}

func HermesControlResponse(contextText string) map[string]any {
	if strings.TrimSpace(contextText) == "" {
		return map[string]any{}
	}
	return map[string]any{"context": contextText}
}
