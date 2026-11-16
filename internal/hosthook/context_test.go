package hosthook

import (
	"context"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/localservice"
)

type contextCall struct {
	tools []string
	args  []map[string]any
}

func (fake *contextCall) Call(_ context.Context, _ string, lineage localservice.Lineage,
	tool string, arguments map[string]any, _ string) (map[string]any, error) {
	if lineage.ProjectID == "" || lineage.SessionID != "session-1" {
		return map[string]any{"ok": false}, nil
	}
	fake.tools = append(fake.tools, tool)
	fake.args = append(fake.args, arguments)
	if tool == "resolve_active_mask" {
		return map[string]any{"ok": true, "lease": map[string]any{"trace": map[string]any{"resolution_status": "missing"}}}, nil
	}
	return map[string]any{"ok": true, "items": []any{
		map[string]any{"title": "Private", "status": "candidate", "text": "hidden", "tags": []any{"host_hook"}},
		map[string]any{"title": "Useful", "status": "active", "text": "verified fact", "tags": []any{"fact"}},
	}}, nil
}

func TestCodexPromptContextFiltersHookRecordsAndBoundsQuery(t *testing.T) {
	root := t.TempDir()
	fake := &contextCall{}
	prompt := strings.Repeat("p", 3000)
	contextText, err := ReadCodexPromptContext(context.Background(), map[string]any{"prompt": prompt},
		Event{EventName: "UserPromptSubmit", SessionID: "session-1"}, root,
		localservice.Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team", Backend: "hindsight"}, fake)
	if err != nil || len(fake.tools) != 2 || fake.tools[0] != "resolve_active_mask" || fake.tools[1] != "build_context" ||
		len([]rune(fake.args[1]["query"].(string))) != promptQueryMaxChars ||
		!strings.Contains(contextText, "verified fact") || strings.Contains(contextText, "hidden") {
		t.Fatalf("prompt context used wrong scope or content: %q, %+v, %v", contextText, fake, err)
	}
	if len([]rune(contextText)) > promptContextMaxChars {
		t.Fatal("prompt context exceeded host limit")
	}
}
