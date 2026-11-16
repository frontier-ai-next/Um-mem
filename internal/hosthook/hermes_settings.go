package hosthook

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/localservice"
	"gopkg.in/yaml.v3"
)

var hermesRegistrations = []HookRegistration{
	{Event: "on_session_start", Timeout: 5},
	{Event: "pre_llm_call", Timeout: 12},
	{Event: "post_llm_call", Timeout: 8},
	{Event: "pre_tool_call", Timeout: 5},
	{Event: "post_tool_call", Timeout: 5},
	{Event: "post_compaction", Timeout: 5},
	{Event: "subagent_stop", Timeout: 5},
	{Event: "on_session_finalize", Timeout: 5},
}

func hermesSettingsPath(home string) (string, error) {
	if strings.TrimSpace(home) == "" {
		return "", errors.New("Hermes home is required")
	}
	root, err := filepath.Abs(home)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "config.yaml"), nil
}

func hermesYAMLValue(mapping *yaml.Node, key string) (*yaml.Node, int) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil, -1
	}
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1], index + 1
		}
	}
	return nil, -1
}

func hermesYAMLSet(mapping *yaml.Node, key string, value *yaml.Node) {
	if _, index := hermesYAMLValue(mapping, key); index >= 0 {
		mapping.Content[index] = value
		return
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func readHermesSettings(path string) (*yaml.Node, *yaml.Node, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	var document yaml.Node
	if len(bytes.TrimSpace(raw)) != 0 {
		if err := yaml.Unmarshal(raw, &document); err != nil {
			return nil, nil, err
		}
	}
	if len(document.Content) == 0 {
		document = yaml.Node{Kind: yaml.DocumentNode,
			Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := document.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
		root = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		document.Content[0] = root
	}
	if root.Kind != yaml.MappingNode {
		return nil, nil, errors.New("Hermes config.yaml must contain a mapping")
	}
	hooks, _ := hermesYAMLValue(root, "hooks")
	if hooks == nil {
		hooks = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		hermesYAMLSet(root, "hooks", hooks)
	}
	if hooks.Kind != yaml.MappingNode {
		return nil, nil, errors.New("Hermes hooks setting must be a mapping")
	}
	return &document, hooks, nil
}

func hermesManagedEntry(entry *yaml.Node) bool {
	if entry.Kind != yaml.MappingNode {
		return false
	}
	command, _ := hermesYAMLValue(entry, "command")
	return command != nil && command.Kind == yaml.ScalarNode && goHandler(command.Value, "hermes")
}

func writeHermesSettings(path string, document *yaml.Node) error {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".hermes-hooks-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(output.Bytes()); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// InstallHermesShellHooks installs only UM-MEM-owned shell callbacks. Hermes
// executes this Go binary directly; the user's Python Hermes runtime remains
// an external host dependency, never part of UM-MEM.
func InstallHermesShellHooks(home, executable string, identity localservice.Identity) (string, error) {
	identity, err := validateHookIdentity(identity)
	if err != nil {
		return "", err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(executable); err != nil || info.IsDir() {
		return "", errors.New("Hermes hook executable is unavailable")
	}
	path, err := hermesSettingsPath(home)
	if err != nil {
		return "", err
	}
	document, hooks, err := readHermesSettings(path)
	if err != nil {
		return "", err
	}
	command, _ := commandFor(executable, "hermes", identity)
	for _, registration := range hermesRegistrations {
		sequence, _ := hermesYAMLValue(hooks, registration.Event)
		if sequence == nil {
			sequence = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			hermesYAMLSet(hooks, registration.Event, sequence)
		}
		if sequence.Kind != yaml.SequenceNode {
			return "", errors.New("Hermes hook event must contain a list")
		}
		kept := make([]*yaml.Node, 0, len(sequence.Content)+1)
		for _, entry := range sequence.Content {
			if !hermesManagedEntry(entry) {
				kept = append(kept, entry)
			}
		}
		entry := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		hermesYAMLSet(entry, "command", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: command})
		hermesYAMLSet(entry, "timeout", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int",
			Value: strconv.Itoa(registration.Timeout)})
		sequence.Content = append(kept, entry)
	}
	if err := writeHermesSettings(path, document); err != nil {
		return "", err
	}
	return path, nil
}

func HermesShellHookStatus(home string) (HookSettingsStatus, error) {
	path, err := hermesSettingsPath(home)
	if err != nil {
		return HookSettingsStatus{}, err
	}
	_, hooks, err := readHermesSettings(path)
	if err != nil {
		return HookSettingsStatus{}, err
	}
	status := HookSettingsStatus{Path: path, InstalledEvents: []string{}, MissingEvents: []string{}}
	for _, registration := range hermesRegistrations {
		found := false
		sequence, _ := hermesYAMLValue(hooks, registration.Event)
		if sequence != nil && sequence.Kind == yaml.SequenceNode {
			for _, entry := range sequence.Content {
				found = found || hermesManagedEntry(entry)
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
