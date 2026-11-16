package blockcatalog

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
	"gopkg.in/yaml.v3"
)

func relatedIDs(block memory.Block) []string {
	if block.GraphPolicy == nil {
		return nil
	}
	return textList(block.GraphPolicy["related_block_ids"])
}

// ValidateTree checks declared parents, materialized child hints, explicit
// relations and containment cycles before a catalog can guide writes.
func ValidateTree(blocks []memory.Block) error {
	byID := make(map[string]memory.Block, len(blocks))
	adjacency := make(map[string]map[string]bool, len(blocks))
	for _, block := range blocks {
		if block.BlockID == "" {
			return errors.New("memory block ID is required")
		}
		if _, exists := byID[block.BlockID]; exists {
			return fmt.Errorf("duplicate memory block ID %q", block.BlockID)
		}
		byID[block.BlockID] = block
		adjacency[block.BlockID] = map[string]bool{}
	}
	for _, block := range blocks {
		if block.ParentID != "" {
			if _, exists := byID[block.ParentID]; !exists {
				return fmt.Errorf("block %q references missing parent %q", block.BlockID, block.ParentID)
			}
			adjacency[block.ParentID][block.BlockID] = true
		}
		for _, childID := range block.ChildIDs {
			child, exists := byID[childID]
			if !exists {
				return fmt.Errorf("block %q references missing child %q", block.BlockID, childID)
			}
			if child.ParentID != "" && child.ParentID != block.BlockID {
				return fmt.Errorf("block %q declares parent %q but %q lists it as a child", childID, child.ParentID, block.BlockID)
			}
			adjacency[block.BlockID][childID] = true
		}
		for _, relatedID := range relatedIDs(block) {
			if relatedID == block.BlockID {
				return fmt.Errorf("block %q cannot relate to itself", block.BlockID)
			}
			if _, exists := byID[relatedID]; !exists {
				return fmt.Errorf("block %q references missing related block %q", block.BlockID, relatedID)
			}
		}
	}
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("invalid memory block hierarchy: cycle detected at %q", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for childID := range adjacency[id] {
			if err := visit(childID); err != nil {
				return err
			}
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	for id := range byID {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func ValidateLeaves(blocks []memory.Block) error {
	parents := map[string]bool{}
	for _, block := range blocks {
		if block.ParentID != "" {
			parents[block.ParentID] = true
		}
		if len(block.ChildIDs) > 0 {
			parents[block.BlockID] = true
		}
	}
	var issues []string
	for _, block := range blocks {
		if parents[block.BlockID] {
			continue
		}
		missing := make([]string, 0, 5)
		if block.Producer == "" {
			missing = append(missing, "producer")
		}
		if len(block.Consumers) == 0 {
			missing = append(missing, "consumers")
		}
		if block.RecallPath == "" {
			missing = append(missing, "recall_path")
		}
		if len(block.LifecyclePolicy) == 0 {
			missing = append(missing, "lifecycle_policy")
		}
		if len(block.IsolationPolicy) == 0 {
			missing = append(missing, "isolation_policy")
		}
		if len(missing) > 0 {
			issues = append(issues, block.BlockID+": "+strings.Join(missing, ", "))
		}
	}
	if len(issues) > 0 {
		return errors.New("leaf block operational contract is incomplete: " + strings.Join(issues, "; "))
	}
	return nil
}

func validateTaxonomy(byID map[string]int) error {
	data, err := packaged.ReadFile("data/taxonomy.yaml")
	if err != nil {
		return err
	}
	var document struct {
		Levels map[string]any `yaml:"levels"`
	}
	if err := yaml.Unmarshal(data, &document); err != nil {
		return err
	}
	if document.Levels == nil {
		return errors.New("invalid memory block taxonomy: levels must be a mapping")
	}
	missing := make([]string, 0)
	for id := range document.Levels {
		if _, exists := byID[id]; !exists {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return errors.New("invalid memory block taxonomy: unknown logical block ids: " + strings.Join(missing, ", "))
	}
	return nil
}
