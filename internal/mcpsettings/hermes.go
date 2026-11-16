package mcpsettings

import (
	"bytes"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

func mappingValue(mapping *yaml.Node, key string) (*yaml.Node, int) {
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

func mappingSet(mapping *yaml.Node, key string, value *yaml.Node) {
	if _, index := mappingValue(mapping, key); index >= 0 {
		mapping.Content[index] = value
		return
	}
	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode,
		Tag: "!!str", Value: key}, value)
}

func yamlNode(value any) (*yaml.Node, error) {
	raw, err := yaml.Marshal(value)
	if err != nil {
		return nil, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil || len(document.Content) != 1 {
		return nil, errors.New("MCP server could not be encoded as YAML")
	}
	return document.Content[0], nil
}

func InstallHermes(path string, config ServerConfig) error {
	raw, err := readSettings(path)
	if err != nil {
		return err
	}
	var document yaml.Node
	if len(bytes.TrimSpace(raw)) != 0 {
		if err := yaml.Unmarshal(raw, &document); err != nil {
			return fmt.Errorf("Hermes settings are not valid YAML: %s: %w", path, err)
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
		return errors.New("Hermes config.yaml must contain a YAML mapping")
	}
	servers, _ := mappingValue(root, "mcp_servers")
	if servers == nil {
		servers = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		mappingSet(root, "mcp_servers", servers)
	}
	if servers.Kind != yaml.MappingNode {
		return errors.New("Hermes mcp_servers setting must be a YAML mapping")
	}
	existing, _ := mappingValue(servers, ServerName)
	if existing != nil {
		var entry map[string]any
		if existing.Kind != yaml.MappingNode {
			return fmt.Errorf("Hermes already has a non-object MCP server named %q", ServerName)
		}
		if err := existing.Decode(&entry); err != nil {
			return err
		}
		if entry == nil {
			entry = map[string]any{}
		}
		if err := managedEntry(entry, "Hermes"); err != nil {
			return err
		}
	}
	if existing == nil {
		entry, err := yamlNode(map[string]any{"command": config.Command,
			"args": config.Args, "env": config.Env, "enabled": true,
			"connect_timeout": 20, "timeout": 120})
		if err != nil {
			return err
		}
		mappingSet(servers, ServerName, entry)
	} else {
		for _, field := range []struct {
			key   string
			value any
		}{{"command", config.Command}, {"args", config.Args}, {"enabled", true},
			{"connect_timeout", 20}, {"timeout", 120}} {
			value, err := yamlNode(field.value)
			if err != nil {
				return err
			}
			mappingSet(existing, field.key, value)
		}
		environment, _ := mappingValue(existing, "env")
		if environment.Kind != yaml.MappingNode {
			return errors.New("Hermes UM-MEM env setting must be a YAML mapping")
		}
		for key, value := range config.Env {
			node, err := yamlNode(value)
			if err != nil {
				return err
			}
			mappingSet(environment, key, node)
		}
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	return writeSettings(path, output.Bytes())
}
