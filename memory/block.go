package memory

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Block is a logical catalog node, independent of a stored MemoryRecord.
type Block struct {
	BlockID             string         `json:"block_id"`
	BlockType           string         `json:"block_type"`
	Temperature         string         `json:"temperature"`
	Scope               string         `json:"scope"`
	Authority           string         `json:"authority"`
	DeliveryPolicy      string         `json:"delivery_policy"`
	Retention           string         `json:"retention"`
	Name                string         `json:"name"`
	Tags                []string       `json:"tags"`
	SemanticMemberships []string       `json:"semantic_memberships"`
	Guidance            map[string]any `json:"guidance"`
	Metadata            map[string]any `json:"metadata"`
	ParentID            string         `json:"parent_id"`
	ChildIDs            []string       `json:"child_ids"`
	IndexOnly           bool           `json:"index_only"`
	AgentWritable       bool           `json:"agent_writable"`
	Owner               string         `json:"owner"`
	Producer            string         `json:"producer"`
	Consumers           []string       `json:"consumers"`
	RecallPath          string         `json:"recall_path"`
	LifecyclePolicy     map[string]any `json:"lifecycle_policy"`
	IsolationPolicy     map[string]any `json:"isolation_policy"`
	AppliesTo           map[string]any `json:"applies_to"`
	RetrievalPolicy     map[string]any `json:"retrieval_policy"`
	GraphPolicy         map[string]any `json:"graph_policy"`
	PromotionPolicy     map[string]any `json:"promotion_policy"`
	TokenBudget         int            `json:"token_budget"`
	StatusPolicy        map[string]any `json:"status_policy"`
}

func (block *Block) Normalize() {
	block.BlockID = strings.TrimSpace(block.BlockID)
	block.Name = strings.TrimSpace(block.Name)
	if block.Name == "" {
		block.Name = block.BlockID
	}
	block.ParentID = strings.TrimSpace(block.ParentID)
	block.Producer = strings.TrimSpace(block.Producer)
	block.RecallPath = strings.TrimSpace(block.RecallPath)
	if block.Tags == nil {
		block.Tags = []string{}
	}
	if block.SemanticMemberships == nil {
		block.SemanticMemberships = []string{}
	}
	if block.ChildIDs == nil {
		block.ChildIDs = []string{}
	}
	if block.Consumers == nil {
		block.Consumers = []string{}
	}
	for _, target := range []*map[string]any{&block.Guidance, &block.Metadata,
		&block.LifecyclePolicy, &block.IsolationPolicy, &block.AppliesTo,
		&block.RetrievalPolicy, &block.GraphPolicy, &block.PromotionPolicy, &block.StatusPolicy} {
		if *target == nil {
			*target = map[string]any{}
		}
	}
	block.Tags = uniqueTexts(block.Tags)
	block.SemanticMemberships = uniqueTexts(block.SemanticMemberships)
	block.ChildIDs = uniqueTexts(block.ChildIDs)
	block.Consumers = uniqueTexts(block.Consumers)
}

func (block Block) Validate() error {
	if block.BlockID == "" || block.Name == "" || block.BlockType == "" {
		return errors.New("catalog block requires ID, name and type")
	}
	if !oneOf(block.Temperature, "hot", "warm", "cold") ||
		!oneOf(block.Scope, "subtask", "task", "session", "workspace", "project", "user_global") ||
		!oneOf(block.DeliveryPolicy, "always_on", "working_set", "on_demand", "reflection_only", "hidden") ||
		!oneOf(block.Retention, "ephemeral", "session", "durable", "archive") {
		return errors.New("catalog block has an unsupported memory axis")
	}
	if block.RecallPath != "" && !oneOf(block.RecallPath, "directive_overlay", "working_state", "hybrid_recall", "exact_lookup", "reflection_queue", "archive_only") {
		return errors.New("catalog block has unsupported recall path")
	}
	if block.TokenBudget < 0 {
		return errors.New("catalog block token budget must be nonnegative")
	}
	for name := range block.Guidance {
		if !oneOf(name, "synopsis", "use_when", "avoid_when") {
			return fmt.Errorf("unknown catalog guidance field %q", name)
		}
	}
	for name := range block.AppliesTo {
		if !oneOf(name, "agents", "workspaces") {
			return fmt.Errorf("unknown catalog applies_to field %q", name)
		}
	}
	for name := range block.LifecyclePolicy {
		if !oneOf(name, "initial_status", "mutable", "terminal_statuses", "expires_at") {
			return fmt.Errorf("unknown catalog lifecycle field %q", name)
		}
	}
	if len(block.LifecyclePolicy) > 0 {
		if strings.TrimSpace(toString(block.LifecyclePolicy["initial_status"])) == "" {
			return errors.New("catalog lifecycle policy requires initial_status")
		}
		if _, ok := block.LifecyclePolicy["mutable"].(bool); !ok {
			return errors.New("catalog lifecycle policy mutable must be boolean")
		}
		statuses := block.LifecyclePolicy["terminal_statuses"]
		switch values := statuses.(type) {
		case []any:
			if len(values) == 0 {
				return errors.New("catalog lifecycle terminal statuses are required")
			}
		case []string:
			if len(values) == 0 {
				return errors.New("catalog lifecycle terminal statuses are required")
			}
		case string:
			if strings.TrimSpace(values) == "" {
				return errors.New("catalog lifecycle terminal statuses are required")
			}
		default:
			return errors.New("catalog lifecycle terminal statuses must be a sequence")
		}
		if expiry := toString(block.LifecyclePolicy["expires_at"]); expiry != "" {
			if _, err := time.Parse(time.RFC3339Nano, expiry); err != nil {
				return errors.New("catalog lifecycle expires_at must be an ISO timestamp")
			}
		}
	}
	for name := range block.IsolationPolicy {
		if !oneOf(name, "dimension", "cross_workspace") {
			return fmt.Errorf("unknown catalog isolation field %q", name)
		}
	}
	if len(block.IsolationPolicy) > 0 {
		dimension := toString(block.IsolationPolicy["dimension"])
		cross, ok := block.IsolationPolicy["cross_workspace"].(bool)
		if !oneOf(dimension, "subtask", "task", "session", "workspace", "project", "user_global") || !ok || cross && dimension != "user_global" {
			return errors.New("catalog isolation policy is invalid")
		}
	}
	return nil
}
