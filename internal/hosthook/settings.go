package hosthook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/localservice"
	"github.com/frontier-ai-next/Um-mem/internal/paths"
)

type HookRegistration struct {
	Event   string
	Matcher string
	Timeout int
}

type HookSettingsStatus struct {
	Installed       bool     `json:"installed"`
	Path            string   `json:"path"`
	InstalledEvents []string `json:"installed_events"`
	MissingEvents   []string `json:"missing_events"`
}

var codexRegistrations = []HookRegistration{
	{Event: "SessionStart", Matcher: "startup|resume|clear|compact"},
	{Event: "UserPromptSubmit"},
	{Event: "PreToolUse", Matcher: ".*"},
	{Event: "PermissionRequest", Matcher: ".*"},
	{Event: "PostToolUse", Matcher: ".*"},
	{Event: "PreCompact", Matcher: "manual|auto"},
	{Event: "PostCompact", Matcher: "manual|auto"},
	{Event: "SubagentStart", Matcher: ".*"},
	{Event: "SubagentStop", Matcher: ".*"},
	{Event: "Stop"},
	{Event: "SessionEnd", Timeout: 3},
}

var claudeRegistrations = []HookRegistration{
	{Event: "SessionStart", Matcher: "startup|resume|clear|compact"},
	{Event: "UserPromptSubmit"},
	{Event: "PreToolUse", Matcher: ".*"},
	{Event: "PostToolUse", Matcher: ".*"},
	{Event: "PostToolUseFailure", Matcher: ".*"},
	{Event: "PostToolBatch"},
	{Event: "SubagentStart", Matcher: ".*"},
	{Event: "SubagentStop", Matcher: ".*"},
	{Event: "TaskCreated"},
	{Event: "TaskCompleted"},
	{Event: "Stop"},
	{Event: "StopFailure", Matcher: ".*"},
	{Event: "PreCompact", Matcher: "manual|auto"},
	{Event: "PostCompact", Matcher: "manual|auto"},
	{Event: "SessionEnd", Matcher: "clear|resume|logout|prompt_input_exit|bypass_permissions_disabled|other"},
	{Event: "WorktreeRemove"},
}

func registrations(host string) ([]HookRegistration, error) {
	switch host {
	case "codex":
		return codexRegistrations, nil
	case "claude":
		return claudeRegistrations, nil
	default:
		return nil, errors.New("hook host must be codex or claude")
	}
}

func settingsPath(project, host string) (string, error) {
	root, err := filepath.Abs(project)
	if err != nil || strings.TrimSpace(project) == "" {
		return "", errors.New("hook project directory is invalid")
	}
	switch host {
	case "codex":
		return filepath.Join(root, ".codex", "hooks.json"), nil
	case "claude":
		return filepath.Join(root, ".claude", "settings.json"), nil
	default:
		return "", errors.New("hook host must be codex or claude")
	}
}

func readHookSettings(path string) (map[string]any, map[string]any, error) {
	settings := map[string]any{}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	if err == nil {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if decodeErr := decoder.Decode(&settings); decodeErr != nil || settings == nil {
			return nil, nil, fmt.Errorf("hook settings are not a JSON object: %s", path)
		}
		var trailing any
		if decodeErr := decoder.Decode(&trailing); decodeErr != io.EOF {
			return nil, nil, fmt.Errorf("hook settings have trailing JSON: %s", path)
		}
	}
	hooks := map[string]any{}
	if rawHooks, exists := settings["hooks"]; exists {
		var ok bool
		hooks, ok = rawHooks.(map[string]any)
		if !ok {
			return nil, nil, errors.New("hook settings 'hooks' must be an object")
		}
	}
	for event, rawGroups := range hooks {
		groups, ok := rawGroups.([]any)
		if !ok {
			return nil, nil, fmt.Errorf("hook event %q must map to a list", event)
		}
		for _, rawGroup := range groups {
			group, ok := rawGroup.(map[string]any)
			if !ok {
				return nil, nil, errors.New("each hook group must be an object")
			}
			handlers, ok := group["hooks"].([]any)
			if !ok {
				return nil, nil, errors.New("each hook group must contain a handler list")
			}
			for _, rawHandler := range handlers {
				if _, ok := rawHandler.(map[string]any); !ok {
					return nil, nil, errors.New("each hook handler must be an object")
				}
			}
		}
	}
	return settings, hooks, nil
}

func ownedHandler(handler map[string]any, host string) bool {
	for _, field := range []string{"command", "commandWindows"} {
		command, _ := handler[field].(string)
		if goHandler(command, host) {
			return true
		}
		legacy := "um_mem.integrations." + host + "_hooks"
		if strings.Contains(command, legacy) {
			return true
		}
	}
	return false
}

func goHandler(command, host string) bool {
	return strings.Contains(command, "--um-mem-hook") && strings.Contains(command, "--host "+host)
}

func removeOwnedHandlers(hooks map[string]any, host string) {
	for event, rawGroups := range hooks {
		groups := rawGroups.([]any)
		retainedGroups := make([]any, 0, len(groups))
		for _, rawGroup := range groups {
			group := rawGroup.(map[string]any)
			handlers := group["hooks"].([]any)
			retainedHandlers := make([]any, 0, len(handlers))
			for _, rawHandler := range handlers {
				if !ownedHandler(rawHandler.(map[string]any), host) {
					retainedHandlers = append(retainedHandlers, rawHandler)
				}
			}
			if len(retainedHandlers) > 0 {
				copyGroup := make(map[string]any, len(group))
				for key, value := range group {
					copyGroup[key] = value
				}
				copyGroup["hooks"] = retainedHandlers
				retainedGroups = append(retainedGroups, copyGroup)
			}
		}
		if len(retainedGroups) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = retainedGroups
		}
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func commandFor(executable, host string, identity localservice.Identity) (string, string) {
	if runtime.GOOS != "windows" {
		command := shellQuote(executable) + " --um-mem-hook --host " + host
		for _, value := range []string{"--repo-root", identity.RepoRoot, "--workspace", identity.WorkspaceID,
			"--profile", identity.ProfileID, "--backend", identity.Backend} {
			command += " " + shellQuote(value)
		}
		return command, command
	}
	// Go's executable path cannot contain a quote on Windows. The first field
	// uses the same command-line quoting style as the legacy Python installer.
	command := `"` + executable + `" --um-mem-hook --host ` + host
	powershell := "& '" + strings.ReplaceAll(executable, "'", "''") + "' --um-mem-hook --host " + host
	values := []string{"--repo-root", identity.RepoRoot, "--workspace", identity.WorkspaceID,
		"--profile", identity.ProfileID, "--backend", identity.Backend}
	for _, value := range values {
		command += ` "` + value + `"`
		powershell += " '" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	return command, powershell
}

func validateHookIdentity(identity localservice.Identity) (localservice.Identity, error) {
	if strings.TrimSpace(identity.RepoRoot) == "" || identity.ProfileID == "" ||
		identity.ProfileID != strings.TrimSpace(identity.ProfileID) ||
		strings.ContainsAny(identity.ProfileID, "/\\\x00\r\n\"%!") ||
		identity.Backend != "mempalace" && identity.Backend != "hindsight" &&
			identity.Backend != "mnemosyne" && identity.Backend != "zvec" {
		return localservice.Identity{}, errors.New("hook identity is invalid")
	}
	workspace, err := paths.NormalizeWorkspaceID(identity.WorkspaceID)
	if err != nil || workspace == "" {
		return localservice.Identity{}, errors.New("hook workspace ID is invalid")
	}
	root, err := filepath.Abs(identity.RepoRoot)
	if err != nil {
		return localservice.Identity{}, err
	}
	if runtime.GOOS == "windows" && strings.ContainsAny(root, "\r\n\"%!") {
		return localservice.Identity{}, errors.New("hook repository root cannot be quoted safely")
	}
	identity.RepoRoot = root
	identity.WorkspaceID = workspace
	return identity, nil
}

// InstallHookSettings merges Go command hooks into host settings. Existing
// Python UM-MEM handlers are replaced only when this explicit cutover command
// is invoked; unrelated host settings and handlers are preserved.
func InstallHookSettings(project, host, executable string, identity localservice.Identity) (string, error) {
	identity, err := validateHookIdentity(identity)
	if err != nil {
		return "", err
	}
	selected, err := registrations(host)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(executable) == "" {
		return "", errors.New("hook executable path is required")
	}
	commandPath, err := filepath.Abs(executable)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(commandPath); statErr != nil || info.IsDir() {
		return "", errors.New("hook executable file is unavailable")
	}
	path, err := settingsPath(project, host)
	if err != nil {
		return "", err
	}
	settings, hooks, err := readHookSettings(path)
	if err != nil {
		return "", err
	}
	removeOwnedHandlers(hooks, host)
	command, windowsCommand := commandFor(commandPath, host, identity)
	for _, registration := range selected {
		timeout := registration.Timeout
		if timeout == 0 {
			timeout = 20
		}
		handler := map[string]any{"type": "command", "command": command, "timeout": timeout}
		if host == "codex" {
			handler["commandWindows"] = windowsCommand
			handler["statusMessage"] = "Capturing UM-MEM lifecycle"
		}
		group := map[string]any{"hooks": []any{handler}}
		if registration.Matcher != "" {
			group["matcher"] = registration.Matcher
		}
		groups, _ := hooks[registration.Event].([]any)
		hooks[registration.Event] = append(groups, group)
	}
	settings["hooks"] = hooks
	content, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", err
	}
	content = append(content, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".hooks-*.tmp")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", err
	}
	return path, nil
}

func HookSettingsState(project, host string) (HookSettingsStatus, error) {
	selected, err := registrations(host)
	if err != nil {
		return HookSettingsStatus{}, err
	}
	path, err := settingsPath(project, host)
	if err != nil {
		return HookSettingsStatus{}, err
	}
	_, hooks, err := readHookSettings(path)
	if err != nil {
		return HookSettingsStatus{}, err
	}
	status := HookSettingsStatus{Path: path, InstalledEvents: []string{}, MissingEvents: []string{}}
	for _, registration := range selected {
		found := false
		if groups, ok := hooks[registration.Event].([]any); ok {
			for _, rawGroup := range groups {
				for _, rawHandler := range rawGroup.(map[string]any)["hooks"].([]any) {
					handler := rawHandler.(map[string]any)
					command, _ := handler["command"].(string)
					windowsCommand, _ := handler["commandWindows"].(string)
					if goHandler(command, host) || goHandler(windowsCommand, host) {
						found = true
					}
				}
			}
		}
		if found {
			status.InstalledEvents = append(status.InstalledEvents, registration.Event)
		} else {
			status.MissingEvents = append(status.MissingEvents, registration.Event)
		}
	}
	sort.Strings(status.InstalledEvents)
	sort.Strings(status.MissingEvents)
	status.Installed = len(status.MissingEvents) == 0
	return status, nil
}
