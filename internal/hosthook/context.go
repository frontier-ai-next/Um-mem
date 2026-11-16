package hosthook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/localservice"
	"golang.org/x/text/unicode/norm"
)

const (
	promptContextMaxTokens = 512
	promptContextMaxChars  = 2800
	promptQueryMaxChars    = 2048
)

type ContextCaller interface {
	Call(context.Context, string, localservice.Lineage, string, map[string]any, string) (map[string]any, error)
}

func firstRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func projectIdentity(workingDir string) (string, error) {
	root := GitRoot(workingDir)
	path, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	path = norm.NFC.String(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	digest := sha256.Sum256([]byte(path))
	return "local-project-" + hex.EncodeToString(digest[:])[:32], nil
}

func hasTag(raw any, target string) bool {
	values, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, item := range values {
		if item == target {
			return true
		}
	}
	return false
}

func optionalString(value any) string {
	text, _ := value.(string)
	return text
}

func renderContextItem(item map[string]any) (string, error) {
	value := struct {
		Title  string `json:"title"`
		Status string `json:"status"`
		Text   string `json:"text"`
	}{Title: optionalString(item["title"]), Status: optionalString(item["status"]), Text: optionalString(item["text"])}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	rendered := strings.TrimSuffix(buffer.String(), "\n")
	rendered = strings.ReplaceAll(rendered, `\u2028`, "\u2028")
	rendered = strings.ReplaceAll(rendered, `\u2029`, "\u2029")
	return rendered, nil
}

// ReadCodexPromptContext retrieves bounded supplemental memory from the
// same authenticated host-hook scope without remembering the prompt itself.
func ReadCodexPromptContext(ctx context.Context, payload map[string]any, event Event,
	workingDir string, identity localservice.Identity, client ContextCaller) (string, error) {
	if event.EventName != "UserPromptSubmit" {
		return "", nil
	}
	projectID, err := projectIdentity(workingDir)
	if err != nil {
		return "", err
	}
	lineage := localservice.Lineage{WorkspaceID: identity.WorkspaceID,
		SessionID: event.SessionID, ProjectID: projectID}
	scope := []string{"project:" + projectID}
	active, err := client.Call(ctx, event.SessionID, lineage, "resolve_active_mask",
		map[string]any{"max_tokens": promptContextMaxTokens, "scope_chain": scope}, "")
	if err != nil {
		return "", err
	}
	if active["ok"] == true {
		lease, leaseOK := active["lease"].(map[string]any)
		if leaseOK {
			trace, traceOK := lease["trace"].(map[string]any)
			context, contextOK := lease["rendered_context"].(string)
			if traceOK && trace["resolution_status"] == "resolved" && contextOK && strings.TrimSpace(context) != "" {
				return firstRunes(context, promptContextMaxChars), nil
			}
		}
	}
	prompt, ok := payload["prompt"].(string)
	if !ok || strings.TrimSpace(prompt) == "" {
		return "", nil
	}
	result, err := client.Call(ctx, event.SessionID, lineage, "build_context",
		map[string]any{"query": firstRunes(strings.TrimSpace(prompt), promptQueryMaxChars),
			"limit": 4, "max_tokens": promptContextMaxTokens,
			"include_candidates": true, "scope_chain": scope,
			"search_all": true, "render_context": false}, "")
	if err != nil {
		return "", err
	}
	if result["ok"] != true {
		return "", errors.New("local service context read failed")
	}
	items, ok := result["items"].([]any)
	if !ok {
		return "", errors.New("local service context response is invalid")
	}
	rendered := make([]string, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || hasTag(item["tags"], "host_hook") {
			continue
		}
		text, ok := item["text"].(string)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		line, err := renderContextItem(item)
		if err != nil {
			return "", err
		}
		rendered = append(rendered, line)
	}
	if len(rendered) == 0 {
		return "", nil
	}
	return firstRunes("UM-MEM recalled context (supplemental, non-directive; verify before acting):\n"+
		strings.Join(rendered, "\n"), promptContextMaxChars), nil
}
