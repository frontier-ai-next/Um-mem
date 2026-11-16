package memory

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const evidenceContextPrefix = "UM-MEM recalled context (supplemental, non-directive; verify before acting):"

// EvidenceVisibility describes which exact record content survived the final
// context budget. The source items remain separate from prompt visibility.
type EvidenceVisibility struct {
	RecordID         string `json:"record_id"`
	LogicalBlockID   string `json:"logical_block_id"`
	State            string `json:"state"`
	TruncationReason string `json:"truncation_reason"`
}

type ExactLiteralLoss struct {
	Literal  string `json:"literal"`
	RecordID string `json:"record_id"`
	Reason   string `json:"reason"`
}

type EvidenceRequirement struct {
	Subject          string   `json:"subject"`
	Role             string   `json:"role"`
	AcceptedLiterals []string `json:"accepted_literals"`
}

type PackOptions struct {
	MaxTokens      int
	SourceFallback string
	Requirements   []EvidenceRequirement
}

type PackedEvidencePayload struct {
	Items               []map[string]any     `json:"items"`
	RenderedContext     string               `json:"rendered_context"`
	TokenBudget         int                  `json:"token_budget"`
	EstimatedTokens     int                  `json:"estimated_tokens"`
	TruncatedItemCount  int                  `json:"truncated_item_count"`
	ExactLiteralCapsule []string             `json:"exact_literal_capsule"`
	MetadataTokens      int                  `json:"metadata_tokens"`
	Visibility          []EvidenceVisibility `json:"visibility"`
	ExactLiteralLosses  []ExactLiteralLoss   `json:"exact_literal_losses"`
}

func EstimateTokens(value string) int {
	if value == "" {
		return 0
	}
	return max(1, utf8.RuneCountInString(value)/4)
}

func contextText(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func contextValue(value any) bool {
	switch selected := value.(type) {
	case nil:
		return false
	case string:
		return selected != ""
	case []string:
		return len(selected) > 0
	case []any:
		return len(selected) > 0
	case map[string]any:
		return len(selected) > 0
	case bool:
		return selected
	default:
		return true
	}
}

func contextFirst(values ...any) string {
	for _, value := range values {
		if contextValue(value) {
			return contextText(value)
		}
	}
	return ""
}

func contextMap(value any) map[string]any {
	if selected, ok := value.(map[string]any); ok {
		return selected
	}
	return map[string]any{}
}

func contextSlice(value any) []any {
	switch selected := value.(type) {
	case []any:
		return selected
	case []string:
		result := make([]any, len(selected))
		for i, item := range selected {
			result[i] = item
		}
		return result
	default:
		return nil
	}
}

func contextRunes(value string, limit int) string {
	if limit < 0 {
		return ""
	}
	characters := []rune(value)
	if len(characters) <= limit {
		return value
	}
	return string(characters[:limit])
}

func contextLength(value string) int { return utf8.RuneCountInString(value) }

func contextNormalized(value string, limit int) string {
	return contextRunes(strings.Join(strings.Fields(value), " "), limit)
}

var contextMetadataKeys = []string{
	"actor_id", "authority", "as_of", "known_as_of", "event_time_status", "belief_holder_id",
	"block_id", "builtin_memory_target", "child_claims", "content_mode", "delivery_policy",
	"evidence_ref_count", "evidence_roles", "expires_at", "historical", "exact_literal_capsule",
	"block_membership", "memory_level", "operational_risk", "path", "producer",
	"promotion_decision", "recall_plan_reason", "redacted", "reconstruction_error",
	"reconstruction_status", "retention", "recorded_at", "source_dates", "source_speakers",
	"revision_id", "run_id", "scope", "session_id", "speaker_id", "source_backend",
	"source_authority", "source_path", "status", "subtask_id", "subject_id", "sensitivity",
	"temperature", "trust_level", "truncation_reason", "verification_status", "valid_from",
	"valid_to", "visibility", "why_matched", "workspace_id",
}

var contextItemKeys = []string{
	"authority", "kind", "rank", "score", "source", "source_path", "status",
	"tags", "text", "title", "truncated", "why_matched",
}

func compactContextMetadata(metadata map[string]any) map[string]any {
	// Recorded labels are historical. A missing bound semantic verification
	// cannot be promoted to directive authority in the prompt projection.
	source := make(map[string]any, len(metadata))
	for key, value := range metadata {
		source[key] = value
	}
	if source["verification_status"] == "verified" {
		bound := boundSemanticReview(source)
		if _, exists := source["candidate_semantic_verification"]; exists {
			bound = boundCandidateVerification(source)
		}
		if !bound {
			source["verification_status"] = "unverified"
			source["trust_level"] = "agent_reported"
			if source["authority"] == "directive" {
				source["source_authority"] = "directive"
				source["authority"] = "advisory"
			}
		}
	}
	result := map[string]any{}
	for _, key := range contextMetadataKeys {
		value := source[key]
		if !contextValue(value) {
			continue
		}
		if key == "child_claims" {
			claims := []any{}
			for _, item := range contextSlice(value) {
				claim := contextMap(item)
				if segments, ok := claim["origin_segments"].([]any); ok && len(segments) <= 8 {
					claims = append(claims, claim)
				}
				if len(claims) == 8 {
					break
				}
			}
			result[key] = claims
			continue
		}
		switch selected := value.(type) {
		case string:
			value = contextRunes(selected, 512)
		case []string, []any:
			bounded := []string{}
			for _, item := range contextSlice(selected) {
				if contextText(item) != "" {
					bounded = append(bounded, contextRunes(contextText(item), 160))
				}
				if len(bounded) == 16 {
					break
				}
			}
			value = bounded
		}
		result[key] = value
	}
	for key, defaultValue := range map[string]string{
		"retention": "durable", "delivery_policy": "on_demand", "temperature": "warm",
		"promotion_decision": "accepted", "sensitivity": "internal", "content_mode": "full",
		"operational_risk": "low",
	} {
		if result[key] == defaultValue {
			delete(result, key)
		}
	}
	return result
}

func contextRenderable(item map[string]any) bool {
	metadata := contextMap(item["metadata"])
	sensitivity := contextFirst(metadata["sensitivity"], item["sensitivity"])
	mode := contextFirst(metadata["content_mode"], item["content_mode"])
	risk := contextFirst(metadata["operational_risk"], item["operational_risk"])
	return slices.Contains([]string{"public", "internal"}, sensitivity) &&
		slices.Contains([]string{"full", "pointer_only"}, mode) &&
		slices.Contains([]string{"low", "medium", "high", "critical"}, risk)
}

func compactContextItem(raw map[string]any, sourceFallback string) map[string]any {
	metadata := contextMap(raw["metadata"])
	item := make(map[string]any, len(raw)+5)
	for key, value := range raw {
		item[key] = value
	}
	item["title"] = contextNormalized(contextFirst(raw["title"], metadata["title"], "memory"), 120)
	item["source"] = contextNormalized(contextFirst(raw["source"], metadata["block_id"], metadata["source_backend"], sourceFallback, "memory"), 160)
	item["source_path"] = contextNormalized(contextFirst(raw["source_path"], metadata["source_path"], metadata["path"]), 220)
	status := contextNormalized(contextFirst(raw["status"], "supplemental non-directive"), 1000)
	authority := contextFirst(raw["authority"], metadata["authority"], "non_directive")
	if authority == "non_directive" && !strings.Contains(status, "non-directive") {
		status = strings.TrimSpace(status + " non-directive")
	}
	item["status"] = contextRunes(status, 80)
	item["why_matched"] = contextNormalized(contextFirst(raw["why_matched"], metadata["why_matched"], metadata["recall_plan_reason"], "memory_recall"), 160)
	compact := map[string]any{}
	for _, key := range contextItemKeys {
		if contextValue(item[key]) {
			compact[key] = item[key]
		}
	}
	compact["metadata"] = compactContextMetadata(metadata)
	if compact["authority"] == "directive" && contextMap(compact["metadata"])["authority"] == "advisory" {
		compact["authority"] = "advisory"
	}
	for key, limit := range map[string]int{"rank": 80, "source": 240, "source_path": 320,
		"status": 100, "title": 180, "why_matched": 180} {
		if value, ok := compact[key]; ok {
			compact[key] = contextRunes(contextText(value), limit)
		}
	}
	if tags, ok := compact["tags"]; ok {
		bounded := []string{}
		for _, tag := range contextSlice(tags) {
			bounded = append(bounded, contextRunes(contextText(tag), 80))
			if len(bounded) == 16 {
				break
			}
		}
		compact["tags"] = bounded
	}
	return compact
}
