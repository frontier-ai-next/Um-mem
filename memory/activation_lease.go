package memory

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
)

type ActivationItem struct {
	BlockID   string         `json:"block_id"`
	Text      string         `json:"text"`
	Source    string         `json:"source"`
	Authority string         `json:"authority"`
	Surface   string         `json:"surface"`
	Lifecycle string         `json:"lifecycle"`
	Score     *float64       `json:"score"`
	Tags      []string       `json:"tags"`
	Metadata  map[string]any `json:"metadata"`
}

func (item ActivationItem) ContextItem() map[string]any {
	return map[string]any{"text": item.Text, "source": item.Source, "score": item.Score,
		"tags": append([]string{}, item.Tags...), "metadata": item.Metadata,
		"authority": item.Authority, "surface": item.Surface, "lifecycle": item.Lifecycle}
}

type ActivationTrace struct {
	Mode                      string               `json:"mode"`
	BackendID                 string               `json:"backend_id"`
	PlanReason                string               `json:"plan_reason"`
	QueryType                 string               `json:"query_type"`
	RequestedTokens           int                  `json:"requested_tokens"`
	AllocatedTokens           int                  `json:"allocated_tokens"`
	EstimatedTokens           int                  `json:"estimated_tokens"`
	MetadataTokens            int                  `json:"metadata_tokens"`
	RenderedItemCount         int                  `json:"rendered_item_count"`
	TruncatedItemCount        int                  `json:"truncated_item_count"`
	LogicalBlockIDs           []string             `json:"logical_block_ids"`
	RecordIDs                 []string             `json:"record_ids"`
	Visibility                []EvidenceVisibility `json:"visibility"`
	ExactLiteralCapsule       []string             `json:"exact_literal_capsule"`
	ExactLiteralLosses        []ExactLiteralLoss   `json:"exact_literal_losses"`
	CorrectiveRecall          map[string]any       `json:"corrective_recall"`
	ReverseProvenance         map[string]any       `json:"reverse_provenance"`
	EventType                 string               `json:"event_type"`
	MaskID                    string               `json:"mask_id"`
	MaskRevision              int                  `json:"mask_revision"`
	CatalogRevision           int                  `json:"catalog_revision"`
	BackendRevision           string               `json:"backend_revision"`
	RefreshReason             string               `json:"refresh_reason"`
	ExcludedLifecycleStatuses []string             `json:"excluded_lifecycle_statuses"`
	ScopeValidationResult     string               `json:"scope_validation_result"`
	ResolutionStatus          string               `json:"resolution_status"`
	SemanticRecallPerformed   bool                 `json:"semantic_recall_performed"`
	OutOfPrompt               bool                 `json:"out_of_prompt"`
}

type ActivationLease struct {
	Query           string           `json:"query"`
	WorkspaceID     string           `json:"workspace_id"`
	RunID           string           `json:"run_id"`
	LogicalBlockIDs []string         `json:"logical_block_ids"`
	Items           []ActivationItem `json:"items"`
	RenderedContext string           `json:"rendered_context"`
	TokenBudget     int              `json:"token_budget"`
	EstimatedTokens int              `json:"estimated_tokens"`
	MetadataTokens  int              `json:"metadata_tokens"`
	Trace           ActivationTrace  `json:"activation_trace"`
	LeaseID         string           `json:"lease_id"`
}

func (lease ActivationLease) ContextItems() []map[string]any {
	items := make([]map[string]any, 0, len(lease.Items))
	for _, item := range lease.Items {
		items = append(items, item.ContextItem())
	}
	return items
}

func ActivationLeaseFromPayload(plan RecallPlan, payload PackedEvidencePayload,
	backendID, mode string) (ActivationLease, error) {
	if mode == "" {
		mode = "fast_one_hop"
	}
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return ActivationLease{}, errors.New("cannot create activation lease ID")
	}
	items := make([]ActivationItem, 0, len(payload.Items))
	corrective := map[string]any{}
	reverse := make([]map[string]any, 0)
	for _, packed := range payload.Items {
		metadata := make(map[string]any)
		for key, value := range contextMap(packed["metadata"]) {
			metadata[key] = value
		}
		metadata["source_authority"] = contextFirst(metadata["source_authority"],
			metadata["authority"], packed["authority"], "advisory")
		metadata["authority"] = "non_directive"
		if next, ok := metadata["corrective_recall"].(map[string]any); ok {
			corrective = make(map[string]any, len(next))
			for key, value := range next {
				corrective[key] = value
			}
		}
		blockID := contextFirst(metadata["block_id"], packed["source"])
		if metadata["reverse_provenance_depth"] == 1 || metadata["reverse_provenance_depth"] == float64(1) {
			reverse = append(reverse, map[string]any{"record_id": blockID,
				"derived_id":     contextFirst(metadata["reverse_provenance_derived_id"]),
				"root_id":        contextFirst(metadata["reverse_provenance_root_id"]),
				"reference_only": metadata["reverse_provenance_reference_only"] == true})
		}
		var score *float64
		switch value := packed["score"].(type) {
		case int:
			selected := float64(value)
			score = &selected
		case float64:
			selected := value
			score = &selected
		}
		tags := make([]string, 0)
		for _, value := range contextSlice(packed["tags"]) {
			if text := contextText(value); text != "" {
				tags = append(tags, text)
			}
		}
		items = append(items, ActivationItem{BlockID: blockID, Text: contextFirst(packed["text"]),
			Source: contextFirst(packed["source"], blockID), Authority: "non_directive",
			Surface: "supplemental_evidence", Lifecycle: contextFirst(packed["lifecycle"], metadata["lifecycle"], "active"),
			Score: score, Tags: tags, Metadata: metadata})
	}
	reverseProvenance := map[string]any{}
	if len(reverse) > 0 {
		reverseProvenance["canonical_source_refs"] = reverse
	}
	trace := ActivationTrace{Mode: mode, BackendID: backendID, PlanReason: plan.Reason,
		QueryType: plan.QueryType, RequestedTokens: plan.MaxTokens,
		AllocatedTokens: payload.TokenBudget, EstimatedTokens: payload.EstimatedTokens,
		MetadataTokens: payload.MetadataTokens, RenderedItemCount: len(payload.Items),
		TruncatedItemCount: payload.TruncatedItemCount,
		LogicalBlockIDs:    append([]string{}, plan.LogicalBlockIDs...), RecordIDs: append([]string{}, plan.RecordIDs...),
		Visibility:          append([]EvidenceVisibility{}, payload.Visibility...),
		ExactLiteralCapsule: append([]string{}, payload.ExactLiteralCapsule...),
		ExactLiteralLosses:  append([]ExactLiteralLoss{}, payload.ExactLiteralLosses...),
		CorrectiveRecall:    corrective, ReverseProvenance: reverseProvenance,
		EventType: "recall_activation", ExcludedLifecycleStatuses: []string{},
		ScopeValidationResult: "matched", ResolutionStatus: "resolved", OutOfPrompt: true}
	return ActivationLease{Query: plan.Query, WorkspaceID: plan.WorkspaceID, RunID: plan.RunID,
		LogicalBlockIDs: append([]string{}, plan.LogicalBlockIDs...), Items: items,
		RenderedContext: payload.RenderedContext, TokenBudget: payload.TokenBudget,
		EstimatedTokens: payload.EstimatedTokens, MetadataTokens: payload.MetadataTokens,
		Trace: trace, LeaseID: "lease_" + hex.EncodeToString(random)}, nil
}
