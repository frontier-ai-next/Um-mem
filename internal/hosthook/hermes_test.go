package hosthook

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/localservice"
)

func TestHermesShellHookParsesBoundedEventAndContext(t *testing.T) {
	root := t.TempDir()
	payload := map[string]any{"hook_event_name": "pre_llm_call", "session_id": "session-1",
		"cwd": root, "profile": "work", "extra": map[string]any{
			"turn_id": "turn-2", "user_message": "What was decided?"}}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	event, kind, message, err := ParseHermes(raw, root)
	if err != nil || kind != "prospective_trigger" || message != "What was decided?" ||
		event.StageID != "turn-2" || event.CapabilityCode != "hermes_shell_hook" {
		t.Fatalf("Hermes event did not bind its host turn: %+v %q %q %v", event, kind, message, err)
	}
	fake := &contextCall{}
	contextText, err := ReadHermesPromptContext(context.Background(), message, event,
		root, localservice.Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team",
			Backend: "hindsight"}, fake)
	if err != nil || len(fake.tools) != 2 || !strings.Contains(contextText, "verified fact") ||
		strings.Contains(contextText, "hidden") {
		t.Fatalf("Hermes prompt context crossed the advisory boundary: %q %+v %v", contextText, fake, err)
	}
	if HermesControlResponse(contextText)["context"] != contextText {
		t.Fatal("Hermes hook did not return the documented context field")
	}
	payload["cwd"] = t.TempDir()
	raw, _ = json.Marshal(payload)
	if _, _, _, err := ParseHermes(raw, root); err == nil {
		t.Fatal("Hermes hook accepted a different working directory")
	}
}
