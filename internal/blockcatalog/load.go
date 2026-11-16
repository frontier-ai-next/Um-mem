package blockcatalog

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/paths"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
	"gopkg.in/yaml.v3"
)

//go:embed data/default_blocks.yaml data/taxonomy.yaml
var packaged embed.FS

type Options struct {
	RepoRoot         string
	WorkspaceID      string
	ProfileID        string
	Strict           bool
	IncludeWorkspace bool
}

var blockFields = func() map[string]bool {
	fields := make(map[string]bool)
	shape := reflect.TypeOf(memory.Block{})
	for i := range shape.NumField() {
		fields[shape.Field(i).Tag.Get("json")] = true
	}
	return fields
}()

func textList(value any) []string {
	var values []any
	switch typed := value.(type) {
	case string:
		for _, item := range strings.Split(strings.ReplaceAll(typed, ";", ","), ",") {
			values = append(values, item)
		}
	case []any:
		values = typed
	case []string:
		for _, item := range typed {
			values = append(values, item)
		}
	case nil:
		return []string{}
	default:
		values = []any{typed}
	}
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, raw := range values {
		item := strings.TrimSpace(fmt.Sprint(raw))
		if item != "" && !seen[item] {
			result = append(result, item)
			seen[item] = true
		}
	}
	return result
}

func coerce(raw map[string]any) (memory.Block, error) {
	if strings.TrimSpace(fmt.Sprint(raw["block_id"])) == "" || raw["block_id"] == nil {
		return memory.Block{}, errors.New("memory block ID is required")
	}
	for key := range raw {
		if !blockFields[key] {
			return memory.Block{}, fmt.Errorf("unknown memory block field %q", key)
		}
	}
	for key, value := range map[string]any{
		"block_type": "generic", "temperature": "warm", "scope": "workspace",
		"authority": "advisory", "delivery_policy": "on_demand", "retention": "durable",
		"owner": "UM-MEM", "agent_writable": true, "token_budget": 1000,
	} {
		if raw[key] == nil || raw[key] == "" {
			raw[key] = value
		}
	}
	if raw["name"] == nil || raw["name"] == "" {
		raw["name"] = raw["block_id"]
	}
	for _, key := range []string{"tags", "semantic_memberships", "child_ids", "consumers"} {
		raw[key] = textList(raw[key])
	}
	if policy, ok := raw["retrieval_policy"].(map[string]any); ok {
		for key := range policy {
			if key != "mode" && key != "limit" && key != "require_query_seed" {
				return memory.Block{}, fmt.Errorf("unknown retrieval_policy field %q", key)
			}
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return memory.Block{}, err
	}
	var block memory.Block
	if err := json.Unmarshal(encoded, &block); err != nil {
		return memory.Block{}, err
	}
	block.Normalize()
	if err := block.Validate(); err != nil {
		return memory.Block{}, err
	}
	return block, nil
}

func parseBlocks(data []byte, strict bool) ([]memory.Block, error) {
	var document any
	if err := yaml.Unmarshal(data, &document); err != nil {
		if strict {
			return nil, err
		}
		return nil, nil
	}
	var rawBlocks any
	switch root := document.(type) {
	case map[string]any:
		rawBlocks = root["blocks"]
	case []any:
		rawBlocks = root
	}
	entries, ok := rawBlocks.([]any)
	if !ok {
		if strict {
			return nil, errors.New("memory block catalog must contain a blocks list")
		}
		return nil, nil
	}
	blocks := make([]memory.Block, 0, len(entries))
	for _, item := range entries {
		raw, ok := item.(map[string]any)
		if !ok {
			if strict {
				return nil, errors.New("memory block entry must be a mapping")
			}
			continue
		}
		block, err := coerce(raw)
		if err != nil {
			if strict {
				return nil, err
			}
			continue
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func readOptional(path string, strict bool) ([]memory.Block, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		if strict {
			return nil, err
		}
		return nil, nil
	}
	return parseBlocks(data, strict)
}

// Load overlays packaged policy blocks, manager and workspace YAML, then the
// active profile-scoped SQLite topology in the same order as the Python registry.
func Load(options Options) ([]memory.Block, error) {
	if strings.TrimSpace(options.RepoRoot) == "" {
		return nil, errors.New("repo root is required")
	}
	profileID := strings.TrimSpace(options.ProfileID)
	if profileID == "" {
		profileID = "default"
	}
	workspaceID := ""
	if options.WorkspaceID != "" {
		var err error
		workspaceID, err = paths.NormalizeWorkspaceID(options.WorkspaceID)
		if err != nil || workspaceID == "" {
			return nil, errors.New("workspace ID is invalid")
		}
	}
	data, err := packaged.ReadFile("data/default_blocks.yaml")
	if err != nil {
		return nil, err
	}
	base, err := parseBlocks(data, true)
	if err != nil {
		return nil, fmt.Errorf("packaged memory blocks: %w", err)
	}
	byID := make(map[string]int, len(base))
	blocks := make([]memory.Block, 0, len(base)+8)
	appendBlocks := func(source []memory.Block, override bool) {
		for _, block := range source {
			if index, found := byID[block.BlockID]; found {
				if override && blocks[index].Authority == "directive_candidate" && block.Authority != "directive_candidate" {
					continue
				}
				blocks[index] = block
			} else {
				byID[block.BlockID] = len(blocks)
				blocks = append(blocks, block)
			}
		}
	}
	appendBlocks(base, false)
	sources := []string{filepath.Join(options.RepoRoot, ".UM-MEM", "memory", "blocks.yaml")}
	if workspaceID != "" {
		sources = append(sources, filepath.Join(options.RepoRoot, "workspaces", workspaceID, ".memory", "blocks.yaml"))
	}
	for _, source := range sources {
		override, err := readOptional(source, options.Strict)
		if err != nil {
			return nil, fmt.Errorf("memory block catalog %s: %w", source, err)
		}
		appendBlocks(override, true)
	}
	if workspaceID != "" && options.IncludeWorkspace {
		store, err := catalog.Open(options.RepoRoot, workspaceID, profileID)
		if err != nil {
			return nil, err
		}
		states, listErr := store.ListBlocks(context.Background(), false)
		closeErr := store.Close()
		if listErr != nil {
			return nil, listErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		for _, state := range states {
			block := state.Block
			if _, found := byID[block.BlockID]; found {
				return nil, fmt.Errorf("dynamic memory block collides with protected catalog block: %s", block.BlockID)
			}
			byID[block.BlockID] = len(blocks)
			blocks = append(blocks, block)
		}
	}
	if err := ValidateTree(blocks); err != nil {
		return nil, err
	}
	if err := ValidateLeaves(blocks); err != nil {
		return nil, err
	}
	if err := validateTaxonomy(byID); err != nil {
		return nil, err
	}
	return blocks, nil
}
