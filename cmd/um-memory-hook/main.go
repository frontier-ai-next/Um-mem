package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/hosthook"
	"github.com/frontier-ai-next/Um-mem/internal/localservice"
)

const reviewGuidance = "Before ending, review this completed work for durable UM-MEM knowledge using the memory " +
	"tools available to you. Save or amend only a supported, reusable fact, user preference, " +
	"decision, correction, or verified lesson. Check the relevant existing block and record " +
	"before adding a duplicate; create a fitting leaf if the clean store has none. If nothing " +
	"durable emerged, save nothing. Do not repeat the answer to the user; finish after this review."

type options struct {
	host       string
	repoRoot   string
	workspace  string
	profile    string
	backend    string
	projectDir string
	install    bool
	status     bool
	adapter    bool
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "um-memory-hook:", err)
		os.Exit(1)
	}
}

func optionsFromArgs(args []string) (options, error) {
	flags := flag.NewFlagSet("um-memory-hook", flag.ContinueOnError)
	var selected options
	flags.StringVar(&selected.host, "host", "", "hook host: codex, claude or hermes")
	flags.StringVar(&selected.repoRoot, "repo-root", "", "repository root")
	flags.StringVar(&selected.workspace, "workspace", "", "workspace ID")
	flags.StringVar(&selected.profile, "profile", "", "profile ID")
	flags.StringVar(&selected.backend, "backend", "", "selected backend")
	flags.StringVar(&selected.projectDir, "project-dir", "", "project hook settings directory, or Hermes home")
	flags.BoolVar(&selected.install, "install", false, "install this Go hook into project host settings")
	flags.BoolVar(&selected.status, "status", false, "report Go hook installation coverage")
	flags.BoolVar(&selected.adapter, "um-mem-hook", false, "UM-MEM owned hook handler marker")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 || selected.host != "codex" && selected.host != "claude" && selected.host != "hermes" {
		return options{}, errors.New("--host must be codex, claude or hermes")
	}
	if selected.install && selected.status {
		return options{}, errors.New("--install and --status cannot be combined")
	}
	return selected, nil
}

func identityFor(selected options, cwd string) (localservice.Identity, error) {
	if _, legacy := os.LookupEnv("UM_MEM_MEMORY_BACKEND"); legacy {
		return localservice.Identity{}, errors.New("UM_MEM_MEMORY_BACKEND is unsupported")
	}
	root := strings.TrimSpace(selected.repoRoot)
	if root == "" {
		root = hosthook.GitRoot(cwd)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return localservice.Identity{}, err
	}
	workspace := strings.TrimSpace(selected.workspace)
	if workspace == "" {
		workspace = strings.TrimSpace(os.Getenv("UM_MEM_WORKSPACE"))
	}
	if workspace == "" {
		workspace = "default"
	}
	profile := strings.TrimSpace(selected.profile)
	if profile == "" {
		profile = strings.TrimSpace(os.Getenv("UM_MEM_PROFILE"))
	}
	if profile == "" {
		profile = "default"
	}
	backend := strings.ToLower(strings.TrimSpace(selected.backend))
	if backend == "" {
		backend = strings.ToLower(strings.TrimSpace(os.Getenv("UM_MEM_BACKEND")))
	}
	if backend == "" {
		backend = "mempalace"
	}
	if backend != "mempalace" && backend != "hindsight" && backend != "mnemosyne" && backend != "zvec" {
		return localservice.Identity{}, errors.New("selected backend is unsupported")
	}
	return localservice.Identity{RepoRoot: root, WorkspaceID: workspace,
		ProfileID: profile, Backend: backend}, nil
}

func capture(ctx context.Context, identity localservice.Identity, event hosthook.Event, kind string,
	timeout time.Duration) bool {
	client, err := localservice.NewClient(identity, "host_state", timeout)
	if err != nil {
		return false
	}
	defer client.Close()
	payload, err := event.HostStatePayload(kind)
	if err != nil {
		return false
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	var request map[string]any
	if err := json.Unmarshal(raw, &request); err != nil {
		return false
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, err := client.SubmitHostState(bounded, event.SessionID, request)
	return err == nil && response["ok"] == true
}

func run(args []string, input io.Reader, output io.Writer) error {
	selected, err := optionsFromArgs(args)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if selected.install || selected.status {
		if selected.host == "hermes" {
			home := selected.projectDir
			if home == "" {
				home = os.Getenv("HERMES_HOME")
			}
			if home == "" {
				userHome, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				home = filepath.Join(userHome, ".hermes")
			}
			if selected.install {
				identity, err := identityFor(selected, cwd)
				if err != nil {
					return err
				}
				executable, err := os.Executable()
				if err != nil {
					return err
				}
				if _, err := hosthook.InstallHermesShellHooks(home, executable, identity); err != nil {
					return err
				}
			}
			state, err := hosthook.HermesShellHookStatus(home)
			if err != nil {
				return err
			}
			return json.NewEncoder(output).Encode(state)
		}
		project := selected.projectDir
		if project == "" {
			project = hosthook.GitRoot(cwd)
		}
		if selected.install {
			identity, err := identityFor(selected, cwd)
			if err != nil {
				return err
			}
			executable, err := os.Executable()
			if err != nil {
				return err
			}
			if _, err := hosthook.InstallHookSettings(project, selected.host, executable, identity); err != nil {
				return err
			}
		}
		state, err := hosthook.HookSettingsState(project, selected.host)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(state)
	}
	raw, err := io.ReadAll(io.LimitReader(input, hosthook.MaxHookInputBytes+1))
	if err != nil {
		return err
	}
	if selected.host == "claude" {
		return runClaude(raw, cwd, selected, output)
	}
	if selected.host == "hermes" {
		return runHermes(raw, cwd, selected, output)
	}
	return runCodex(raw, cwd, selected, output)
}

func runHermes(raw []byte, cwd string, selected options, output io.Writer) error {
	event, kind, message, err := hosthook.ParseHermes(raw, cwd)
	if err != nil {
		return json.NewEncoder(output).Encode(map[string]any{})
	}
	identity, err := identityFor(selected, cwd)
	if err != nil {
		return json.NewEncoder(output).Encode(map[string]any{})
	}
	_ = capture(context.Background(), identity, event, kind, 8*time.Second)
	contextText := ""
	if event.EventName == "pre_llm_call" {
		client, clientErr := localservice.NewClient(identity, "host_hook", 6*time.Second)
		if clientErr == nil {
			bounded, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			contextText, clientErr = hosthook.ReadHermesPromptContext(bounded, message,
				event, cwd, identity, client)
			cancel()
			client.Close()
		}
		if clientErr != nil {
			contextText = ""
		}
	}
	return json.NewEncoder(output).Encode(hosthook.HermesControlResponse(contextText))
}

func runCodex(raw []byte, cwd string, selected options, output io.Writer) error {
	event, kind, parseErr := hosthook.ParseCodex(raw, cwd)
	eventName := event.EventName
	if parseErr != nil && len(raw) <= hosthook.MaxHookInputBytes {
		var payload map[string]any
		if json.Unmarshal(raw, &payload) == nil {
			eventName, _ = payload["hook_event_name"].(string)
		}
	}
	errorText, contextText := "", ""
	if parseErr != nil {
		errorText = parseErr.Error()
	} else {
		identity, err := identityFor(selected, cwd)
		if err != nil {
			errorText = "local service identity rejected"
		} else {
			timeout := 15 * time.Second
			if event.EventName == "UserPromptSubmit" {
				timeout = 8 * time.Second
			}
			if !capture(context.Background(), identity, event, kind, timeout) {
				errorText = "local service host-state scheduling failed"
			}
			if event.EventName == "UserPromptSubmit" {
				var payload map[string]any
				if json.Unmarshal(raw, &payload) == nil {
					client, clientErr := localservice.NewClient(identity, "host_hook", 6*time.Second)
					if clientErr == nil {
						bounded, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						contextText, clientErr = hosthook.ReadCodexPromptContext(bounded, payload, event, cwd, identity, client)
						cancel()
						client.Close()
					}
					if clientErr != nil {
						errorText = "local service context read failed"
					}
				}
			}
		}
	}
	return json.NewEncoder(output).Encode(hosthook.CodexControlResponse(eventName, contextText, errorText))
}

func runClaude(raw []byte, cwd string, selected options, output io.Writer) error {
	event, kind, err := hosthook.ParseClaude(raw, cwd, nil)
	if err != nil {
		return nil
	}
	identity, err := identityFor(selected, cwd)
	if err != nil || !capture(context.Background(), identity, event, kind, 15*time.Second) {
		return nil
	}
	if event.EventName == "Stop" {
		var payload map[string]any
		if json.Unmarshal(raw, &payload) == nil && payload["stop_hook_active"] == false {
			return json.NewEncoder(output).Encode(map[string]any{"hookSpecificOutput": map[string]any{
				"hookEventName": "Stop", "additionalContext": reviewGuidance}})
		}
	}
	return nil
}
