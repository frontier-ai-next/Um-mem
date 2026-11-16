package memory

import (
	"fmt"
	"slices"
	"strings"
)

// WriteProposal is the caller's explicit request to place agent-authored
// evidence in a logical catalog block. Routing never silently changes the
// selected block based on the content or kind.
type WriteProposal struct {
	Title            string           `json:"title"`
	Content          string           `json:"content"`
	Kind             string           `json:"kind"`
	Tags             []string         `json:"tags"`
	WorkspaceID      string           `json:"workspace_id"`
	RunID            string           `json:"run_id"`
	StageID          string           `json:"stage_id"`
	SubtaskID        string           `json:"subtask_id"`
	SourceID         string           `json:"source_id"`
	EvidenceKind     string           `json:"evidence_kind"`
	EvidenceRefs     []map[string]any `json:"evidence_refs"`
	WriteMode        string           `json:"write_mode"`
	RequestedBlockID string           `json:"requested_block_id"`
	Metadata         map[string]any   `json:"metadata"`
}

type RouteDecision struct {
	Accepted            bool     `json:"accepted"`
	Reason              string   `json:"reason"`
	BlockID             string   `json:"block_id"`
	Retention           string   `json:"retention"`
	DeliveryPolicy      string   `json:"delivery_policy"`
	Temperature         string   `json:"temperature"`
	Authority           string   `json:"authority"`
	Scope               string   `json:"scope"`
	TrustLevel          string   `json:"trust_level"`
	VerificationStatus  string   `json:"verification_status"`
	VerificationMethods []string `json:"verification_methods"`
	PromotionDecision   string   `json:"promotion_decision"`
	Status              string   `json:"status"`
	Tags                []string `json:"tags"`
	PolicyIssues        []string `json:"policy_issues"`
	CatalogRevisionID   string   `json:"catalog_revision_id"`
	SuggestedBlockID    string   `json:"suggested_block_id"`
	SuggestedAction     string   `json:"suggested_action"`
}

func distinctInOrder(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func newRouteDecision(block Block, tags []string) RouteDecision {
	return RouteDecision{BlockID: block.BlockID, Retention: block.Retention,
		DeliveryPolicy: block.DeliveryPolicy, Temperature: block.Temperature,
		Authority: "advisory", Scope: block.Scope, TrustLevel: "agent_reported",
		VerificationStatus: "unverified", VerificationMethods: []string{},
		PromotionDecision: "not_requested", Status: "candidate", Tags: tags,
		PolicyIssues: []string{}}
}

func (decision *RouteDecision) reject(reason string, issues []string, suggestion string) {
	decision.Reason, decision.PolicyIssues = reason, issues
	if suggestion != "" {
		decision.SuggestedBlockID = suggestion
		decision.SuggestedAction = "retry_remember"
	}
}

// RouteMemoryWrite matches the Python agent-authored routing policy. A rejected
// decision is returned as data; malformed catalog state is returned as error.
func RouteMemoryWrite(write WriteProposal, blocks []Block) (RouteDecision, error) {
	catalog, err := CompileSemanticCatalog(blocks)
	if err != nil {
		return RouteDecision{}, fmt.Errorf("compile routing catalog: %w", err)
	}
	tags := distinctInOrder(append(append([]string{}, write.Tags...), write.Kind))
	requestedID := strings.TrimSpace(write.RequestedBlockID)
	if requestedID == "" {
		decision := newRouteDecision(Block{Scope: "workspace", Retention: "durable",
			DeliveryPolicy: "on_demand", Temperature: "warm"}, tags)
		decision.CatalogRevisionID = catalog.RevisionID
		decision.reject("requested_block_id_required", []string{"requested_block_id_required"}, "")
		return decision, nil
	}
	byID := make(map[string]Block, len(blocks))
	parentIDs := make(map[string]bool)
	for _, block := range blocks {
		byID[block.BlockID] = block
		if block.ParentID != "" {
			parentIDs[block.ParentID] = true
		}
	}
	selected, found := byID[requestedID]
	if !found {
		decision := newRouteDecision(Block{BlockID: requestedID, Scope: "workspace",
			Retention: "durable", DeliveryPolicy: "on_demand", Temperature: "warm"}, tags)
		decision.CatalogRevisionID = catalog.RevisionID
		decision.reject("unknown_requested_block", []string{"unknown_logical_block_id"}, "")
		return decision, nil
	}
	decision := newRouteDecision(selected, tags)
	decision.CatalogRevisionID = catalog.RevisionID
	leafIDs := make([]string, 0)
	for _, block := range blocks {
		if block.AgentWritable && !block.IndexOnly && block.Authority == "advisory" &&
			len(block.ChildIDs) == 0 && !parentIDs[block.BlockID] {
			leafIDs = append(leafIDs, block.BlockID)
		}
	}
	slices.Sort(leafIDs)
	suggested := ""
	if slices.Contains(leafIDs, "decisions_and_contracts") {
		suggested = "decisions_and_contracts"
	} else if len(leafIDs) > 0 {
		suggested = leafIDs[0]
	}
	reserved := make([]string, 0)
	for key := range write.Metadata {
		if reservedMetadataKey(key) {
			reserved = append(reserved, key)
		}
	}
	slices.Sort(reserved)
	if len(reserved) > 0 {
		issues := make([]string, len(reserved))
		for i, key := range reserved {
			issues[i] = "reserved_metadata:" + key
		}
		decision.reject("reserved_metadata_governance_field", issues, "")
		return decision, nil
	}
	if requestedID == "verified_lessons" {
		reflection := byID["reflection_candidates"]
		if reflection.BlockID == "" || !reflection.AgentWritable || reflection.IndexOnly {
			reflection.BlockID = ""
		}
		decision.reject("verified_lesson_requires_typed_promotion",
			[]string{"typed_verification_and_tribunal_required"}, reflection.BlockID)
		return decision, nil
	}
	if selected.IndexOnly {
		decision.reject("index_only_block_cannot_store_records", []string{"index_only_not_record_storage"}, suggested)
		return decision, nil
	}
	if !selected.AgentWritable {
		decision.reject("host_managed_block_cannot_accept_agent_writes", []string{"host_managed_block"}, suggested)
		return decision, nil
	}
	if len(selected.ChildIDs) > 0 || parentIDs[selected.BlockID] {
		decision.reject("non_leaf_block_cannot_store_records", []string{"non_leaf_not_record_storage"}, suggested)
		return decision, nil
	}
	if selected.Authority != "advisory" {
		decision.reject("agent_write_requires_typed_promotion_route", []string{"typed_promotion_route_required"}, suggested)
		return decision, nil
	}
	decision.Accepted = true
	decision.Reason = "requested_block_policy"
	return decision, nil
}
