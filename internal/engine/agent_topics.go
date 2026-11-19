package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/frontier-ai-next/Um-mem/memory"
)

type AgentTopic struct {
	BlockID string   `json:"block_id"`
	Path    []string `json:"path"`
	Index   bool     `json:"index"`
}

func agentTopicRoot(blocks []memory.Block) string {
	for _, block := range blocks {
		if block.BlockType == "workspace_profile_root" {
			return block.BlockID
		}
	}
	return ""
}

// EnsureAgentTopic resolves an agent-authored path under the workspace root.
// Only the last segment stores records, so adding subtopics preserves old IDs.
func (engine *Engine) EnsureAgentTopic(ctx context.Context, path []string, synopsis string) (AgentTopic, error) {
	if len(path) < 1 || len(path) > 5 {
		return AgentTopic{}, errors.New("topic must contain one to five names, ending in a writable block")
	}
	names := make([]string, len(path))
	for i, raw := range path {
		if strings.ContainsFunc(raw, unicode.IsControl) {
			return AgentTopic{}, errors.New("topic names cannot contain control characters")
		}
		names[i] = strings.Join(strings.Fields(raw), " ")
		if names[i] == "" || utf8.RuneCountInString(names[i]) > 72 {
			return AgentTopic{}, errors.New("topic names must contain one to 72 characters")
		}
	}
	lock, err := engine.acquireEraseWorkspaceLock(ctx)
	if err != nil {
		return AgentTopic{}, err
	}
	defer lock.Unlock()
	if err = engine.RefreshCatalog(); err != nil {
		return AgentTopic{}, err
	}
	blocks := engine.Blocks()
	parent := agentTopicRoot(blocks)
	if parent == "" {
		return AgentTopic{}, errors.New("workspace topic root is unavailable")
	}
	for i, name := range names {
		index := i < len(names)-1
		var match *memory.Block
		for j := range blocks {
			block := &blocks[j]
			if block.ParentID != parent || !strings.EqualFold(block.Name, name) {
				continue
			}
			if match != nil {
				return AgentTopic{}, fmt.Errorf("ambiguous topic %q: use its block ID", name)
			}
			match = block
		}
		if match != nil {
			if match.IndexOnly != index || match.Scope != "workspace" || match.Authority != "advisory" ||
				(!index && !match.AgentWritable) {
				return AgentTopic{}, fmt.Errorf("topic %q has another role: select an existing writable leaf or choose a different path", name)
			}
			names[i], parent = match.Name, match.BlockID
			continue
		}
		description := name
		if !index && strings.TrimSpace(synopsis) != "" {
			description = strings.TrimSpace(synopsis)
		}
		created, err := engine.CreateBlock(ctx, CreateBlockRequest{
			Name: name, Synopsis: description, ParentID: parent, IndexOnly: index,
			Reason: "Agent selected a topic for durable evidence",
		})
		if err != nil {
			return AgentTopic{}, err
		}
		parent = created["block_id"].(string)
	}
	return AgentTopic{BlockID: parent, Path: names}, nil
}

// AgentTopics returns a small navigable view of the agent's workspace tree.
func (engine *Engine) AgentTopics(query string, limit int) ([]AgentTopic, error) {
	if err := engine.RefreshCatalog(); err != nil {
		return nil, err
	}
	blocks := engine.Blocks()
	root := agentTopicRoot(blocks)
	byID := make(map[string]memory.Block, len(blocks))
	for _, block := range blocks {
		byID[block.BlockID] = block
	}
	type match struct {
		AgentTopic
		score float64
	}
	words := tokenSet(query)
	matches := make([]match, 0)
	for _, block := range blocks {
		if block.Scope != "workspace" || block.Authority != "advisory" ||
			(!block.IndexOnly && !block.AgentWritable) || block.BlockID == root {
			continue
		}
		path := []string{block.Name}
		parent := block.ParentID
		for parent != "" && parent != root && len(path) <= 8 {
			ancestor := byID[parent]
			path = append([]string{ancestor.Name}, path...)
			parent = ancestor.ParentID
		}
		if parent == "" || parent != root {
			continue
		}
		joined := strings.Join(path, " / ")
		score := containment(words, tokenSet(joined))
		if strings.TrimSpace(query) != "" && strings.Contains(strings.ToLower(joined), strings.ToLower(strings.TrimSpace(query))) {
			score++
		}
		matches = append(matches, match{AgentTopic{block.BlockID, path, block.IndexOnly}, score})
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		if len(matches[i].Path) != len(matches[j].Path) {
			return len(matches[i].Path) < len(matches[j].Path)
		}
		return strings.Join(matches[i].Path, "/") < strings.Join(matches[j].Path, "/")
	})
	if limit <= 0 {
		limit = 8
	}
	results := make([]AgentTopic, 0, min(len(matches), limit, 20))
	for _, candidate := range matches[:min(len(matches), limit, 20)] {
		results = append(results, candidate.AgentTopic)
	}
	return results, nil
}
