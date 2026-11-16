package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// BlockLocalEvidenceRegion is a caller-selected, read-only set of canonical
// records. Primary evidence must belong to the selected logical block.
type BlockLocalEvidenceRegion struct {
	BackendID        string
	WorkspaceID      string
	LogicalBlockID   string
	PrimaryRecordIDs []string
	Records          []MemoryRecord
}

func (region BlockLocalEvidenceRegion) Validate() error {
	if strings.TrimSpace(region.BackendID) == "" || strings.TrimSpace(region.WorkspaceID) == "" ||
		strings.TrimSpace(region.LogicalBlockID) == "" {
		return errors.New("deep reader requires backend, workspace, and logical block identifiers")
	}
	if len(region.PrimaryRecordIDs) == 0 {
		return errors.New("primary_record_ids must contain evidence identifiers")
	}
	byID := make(map[string]MemoryRecord, len(region.Records))
	for _, record := range region.Records {
		if record.BlockID == "" || record.WorkspaceID != region.WorkspaceID ||
			(record.SourceBackend != "" && record.SourceBackend != region.BackendID) {
			return errors.New("evidence region contains a foreign or unidentified record")
		}
		if _, found := byID[record.BlockID]; found {
			return errors.New("evidence region record IDs must be unique")
		}
		byID[record.BlockID] = record
	}
	for _, id := range region.PrimaryRecordIDs {
		if strings.TrimSpace(id) == "" {
			return errors.New("primary_record_ids must contain evidence identifiers")
		}
		record, found := byID[id]
		if !found {
			return fmt.Errorf("evidence region is missing primary record %q", id)
		}
		membership := record.BlockMembership
		if membership == nil {
			return errors.New("primary evidence has no logical block membership")
		}
		inside := membership.PrimaryBlockID == region.LogicalBlockID
		for _, secondary := range membership.SecondaryBlockIDs {
			inside = inside || secondary == region.LogicalBlockID
		}
		if !inside {
			return errors.New("primary evidence is outside the selected logical block")
		}
	}
	return nil
}

type DeepEvidenceItem struct {
	RecordID               string `json:"record_id"`
	Content                string `json:"content"`
	EvidenceKind           string `json:"evidence_kind"`
	SourceRecordID         string `json:"source_record_id"`
	CanonicalContentSHA256 string `json:"canonical_content_sha256"`
	Surface                string `json:"surface"`
	Authority              string `json:"authority"`
	WritePermission        bool   `json:"write_permission"`
	PromotionPermission    bool   `json:"promotion_permission"`
}

type BlockLocalDeepEvidenceResult struct {
	BackendID              string             `json:"backend_id"`
	WorkspaceID            string             `json:"workspace_id"`
	LogicalBlockID         string             `json:"logical_block_id"`
	Items                  []DeepEvidenceItem `json:"items"`
	ReverseProvenanceDepth int                `json:"reverse_provenance_depth"`
	BackendPath            string             `json:"backend_path"`
	FinalOutput            string             `json:"final_output"`
	WritePermission        bool               `json:"write_permission"`
	PromotionPermission    bool               `json:"promotion_permission"`
}

type ReaderGuidanceResolution struct {
	Outcome    string
	Card       map[string]any
	SilentMiss bool
}

func validReaderGuidance(raw any, blockID string) map[string]any {
	descriptor, ok := raw.(map[string]any)
	if !ok || descriptor["block_id"] != blockID {
		return nil
	}
	guidance, ok := descriptor["guidance"].(map[string]any)
	if !ok || guidance["block_id"] != blockID {
		return nil
	}
	synopsis, ok := guidance["synopsis"].(string)
	if !ok || strings.TrimSpace(synopsis) == "" {
		return nil
	}
	useWhen := contextSlice(guidance["use_when"])
	if len(useWhen) == 0 {
		return nil
	}
	uses := make([]string, 0, len(useWhen))
	for _, rawUse := range useWhen {
		use, ok := rawUse.(string)
		if !ok || strings.TrimSpace(use) == "" {
			return nil
		}
		uses = append(uses, use)
	}
	avoidWhen := contextSlice(guidance["avoid_when"])
	avoids := make([]string, 0, len(avoidWhen))
	for _, rawAvoid := range avoidWhen {
		avoid, ok := rawAvoid.(string)
		if !ok {
			return nil
		}
		avoids = append(avoids, avoid)
	}
	return map[string]any{"block_id": blockID, "synopsis": strings.TrimSpace(synopsis),
		"use_when": uses, "avoid_when": avoids}
}

// ResolveReaderGuidance is diagnostic only: descriptor text never enters the
// evidence lease. Invalid current guidance widens visibly to block evidence.
func ResolveReaderGuidance(plan RecallPlan, blockID string) ReaderGuidanceResolution {
	current := contextSlice(plan.Metadata["block_descriptors"])
	matchingCurrent := false
	for _, raw := range current {
		value, ok := raw.(map[string]any)
		if !ok || value["block_id"] != blockID {
			continue
		}
		matchingCurrent = true
		if card := validReaderGuidance(value, blockID); card != nil {
			return ReaderGuidanceResolution{Outcome: "current_guidance", Card: card}
		}
	}
	for _, raw := range contextSlice(plan.Metadata["last_known_good_block_descriptors"]) {
		if card := validReaderGuidance(raw, blockID); card != nil {
			return ReaderGuidanceResolution{Outcome: "last_known_good_guidance", Card: card}
		}
	}
	if matchingCurrent {
		return ReaderGuidanceResolution{Outcome: "incompatible_widened_to_block_evidence"}
	}
	return ReaderGuidanceResolution{Outcome: "missing_widened_to_block_evidence"}
}

type DeepReadTrace struct {
	Mode                   string `json:"mode"`
	BackendID              string `json:"backend_id"`
	LogicalBlockID         string `json:"logical_block_id"`
	Limit                  int    `json:"limit"`
	MaxTokens              int    `json:"max_tokens"`
	ReconstructionSteps    int    `json:"reconstruction_steps"`
	GuidanceOutcome        string `json:"guidance_outcome"`
	SilentGuidanceMiss     bool   `json:"silent_guidance_miss"`
	HostActionsAllowed     bool   `json:"host_actions_allowed"`
	ReverseProvenanceDepth int    `json:"reverse_provenance_depth"`
	FinalOutput            string `json:"final_output"`
	WritePermission        bool   `json:"write_permission"`
	PromotionPermission    bool   `json:"promotion_permission"`
}

type DeepReadResult struct {
	Lease ActivationLease
	Trace DeepReadTrace
}

func (result DeepReadResult) Mapping() map[string]any {
	return map[string]any{"items": result.Lease.ContextItems(),
		"rendered_context": result.Lease.RenderedContext,
		"activation_trace": result.Lease.Trace, "deep_read_trace": result.Trace}
}

func DeepReadResultFromLease(lease ActivationLease, plan RecallPlan,
	backendID, blockID string, limit int) DeepReadResult {
	guidance := ResolveReaderGuidance(plan, blockID)
	reconstruction := 1
	for _, item := range lease.Items {
		if _, ok := item.Metadata["corrective_recall"].(map[string]any); ok {
			reconstruction = 2
		}
		if item.Metadata["reverse_provenance_depth"] == 1 ||
			item.Metadata["reverse_provenance_depth"] == float64(1) {
			reconstruction = 2
		}
	}
	return DeepReadResult{Lease: lease, Trace: DeepReadTrace{Mode: "deep_block_local",
		BackendID: backendID, LogicalBlockID: blockID, Limit: limit,
		MaxTokens: lease.TokenBudget, ReconstructionSteps: reconstruction,
		GuidanceOutcome: guidance.Outcome, SilentGuidanceMiss: guidance.SilentMiss,
		ReverseProvenanceDepth: 1, FinalOutput: "evidence"}}
}

func deepEvidenceItem(record MemoryRecord, kind, sourceID string) DeepEvidenceItem {
	digest := sha256.Sum256([]byte(record.Content))
	return DeepEvidenceItem{RecordID: record.BlockID, Content: record.Content, EvidenceKind: kind,
		SourceRecordID: sourceID, CanonicalContentSHA256: "sha256:" + hex.EncodeToString(digest[:]),
		Surface: "supplemental_evidence", Authority: "advisory"}
}

// ReadBlockLocalEvidenceRegion expands only depth-one source identifiers that
// the caller has already fetched from the selected canonical backend.
func ReadBlockLocalEvidenceRegion(region BlockLocalEvidenceRegion,
	reverseSources func(string) []string, maxPrimaryRecords, maxSourceSpans int) (BlockLocalDeepEvidenceResult, error) {
	if err := region.Validate(); err != nil {
		return BlockLocalDeepEvidenceResult{}, err
	}
	if reverseSources == nil || maxPrimaryRecords < 1 || maxSourceSpans < 1 {
		return BlockLocalDeepEvidenceResult{}, errors.New("deep reader requires source lookup and positive limits")
	}
	byID := make(map[string]MemoryRecord, len(region.Records))
	for _, record := range region.Records {
		byID[record.BlockID] = record
	}
	primaryIDs := region.PrimaryRecordIDs[:min(len(region.PrimaryRecordIDs), maxPrimaryRecords)]
	items := make([]DeepEvidenceItem, 0, len(primaryIDs)+maxSourceSpans)
	seen := make(map[string]bool, len(primaryIDs)+maxSourceSpans)
	for _, id := range primaryIDs {
		items = append(items, deepEvidenceItem(byID[id], "selected_evidence", id))
		seen[id] = true
	}
	sources := 0
	for _, id := range primaryIDs {
		for _, candidate := range reverseSources(id) {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" || seen[candidate] {
				continue
			}
			record, found := byID[candidate]
			if !found {
				continue
			}
			items = append(items, deepEvidenceItem(record, "canonical_raw_span", id))
			seen[candidate] = true
			sources++
			if sources >= maxSourceSpans {
				break
			}
		}
		if sources >= maxSourceSpans {
			break
		}
	}
	return BlockLocalDeepEvidenceResult{BackendID: region.BackendID, WorkspaceID: region.WorkspaceID,
		LogicalBlockID: region.LogicalBlockID, Items: items, ReverseProvenanceDepth: 1,
		BackendPath: "selected_backend", FinalOutput: "evidence"}, nil
}
