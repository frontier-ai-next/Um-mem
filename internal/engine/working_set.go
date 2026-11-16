package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/memory"
)

type WorkingRecordRef struct {
	RecordID       string `json:"record_id"`
	RevisionID     string `json:"revision_id"`
	ContentHash    string `json:"content_hash"`
	LogicalBlockID string `json:"logical_block_id"`
}

type WorkingSetRead struct {
	Records              []WorkingRecordRef `json:"records"`
	RenderedContext      string             `json:"rendered_context"`
	TokenBudget          int                `json:"token_budget"`
	EstimatedTokens      int                `json:"estimated_tokens"`
	ByteBudget           int                `json:"byte_budget"`
	RenderedBytes        int                `json:"rendered_bytes"`
	Truncated            bool               `json:"truncated"`
	OmittedRecordIDs     []string           `json:"omitted_record_ids"`
	UnavailableRecordIDs []string           `json:"unavailable_record_ids"`
	TokenAccounting      string             `json:"token_accounting"`
	Visibility           string             `json:"visibility"`
}

func ownedCandidate(record memory.MemoryRecord, ownerID, generationID string) bool {
	owner, ok := record.Metadata["working_set_owner"].(map[string]any)
	return ok && len(owner) == 2 && owner["owner_id"] == ownerID && owner["generation_id"] == generationID
}

// ReadWorkingSet reads exact owner-bound, unpublished candidate records. It
// returns content only through the shared final evidence pack and leaves the
// canonical lifecycle unchanged. ScopeChain must already be bound to the host.
func (engine *Engine) ReadWorkingSet(ctx context.Context, ownerID, generationID string,
	recordIDs []string, maxTokens, maxBytes int, scopeChain []string) (WorkingSetRead, error) {
	if strings.TrimSpace(ownerID) == "" || strings.TrimSpace(generationID) == "" {
		return WorkingSetRead{}, errors.New("working set requires an exact owner and generation")
	}
	if len(recordIDs) < 1 || len(recordIDs) > 32 {
		return WorkingSetRead{}, errors.New("working set requires between 1 and 32 exact record IDs")
	}
	seen := make(map[string]bool, len(recordIDs))
	for _, id := range recordIDs {
		if strings.TrimSpace(id) == "" {
			return WorkingSetRead{}, errors.New("working set record IDs must be nonempty strings")
		}
		if seen[id] {
			return WorkingSetRead{}, errors.New("working set record IDs must be unique")
		}
		seen[id] = true
	}
	if maxTokens < 1 || maxBytes < 1 {
		return WorkingSetRead{}, errors.New("working set budgets must be positive integers")
	}
	if len(scopeChain) == 0 {
		return WorkingSetRead{}, errors.New("working set requires a bound scope chain")
	}
	result := WorkingSetRead{Records: []WorkingRecordRef{}, TokenBudget: maxTokens,
		ByteBudget: maxBytes, OmittedRecordIDs: []string{}, UnavailableRecordIDs: []string{},
		TokenAccounting: "um_mem_estimate", Visibility: "working_unverified"}
	cards := []map[string]any{}
	selectedIDs := []string{}
	for _, id := range recordIDs {
		record, err := engine.ReadStored(ctx, id)
		if err != nil {
			return WorkingSetRead{}, err
		}
		if record == nil || record.BlockID != id || record.Status != "candidate" ||
			!ownedCandidate(*record, ownerID, generationID) {
			result.UnavailableRecordIDs = append(result.UnavailableRecordIDs, id)
			continue
		}
		plan := memory.NewRecallPlan("")
		plan.WorkspaceID, plan.ProfileID = engine.config.WorkspaceID, engine.config.ProfileID
		plan.ScopeChain = scopeChain
		plan.RunID = record.RunID
		plan.IncludeCandidates = true
		plan.SourceHorizon = "durable"
		if !memory.EligibleForRecall(*record, plan, time.Now()) {
			result.UnavailableRecordIDs = append(result.UnavailableRecordIDs, id)
			continue
		}
		digest := sha256.Sum256([]byte(record.Content))
		blockID := ""
		if record.BlockMembership != nil {
			blockID = record.BlockMembership.PrimaryBlockID
		}
		result.Records = append(result.Records, WorkingRecordRef{
			RecordID: record.BlockID, RevisionID: record.RevisionID,
			ContentHash: hex.EncodeToString(digest[:]), LogicalBlockID: blockID})
		selectedIDs = append(selectedIDs, id)
		cards = append(cards, map[string]any{
			"text": record.Content, "source": record.BlockID, "title": record.Title,
			"status": "candidate", "authority": "advisory", "sensitivity": record.Sensitivity,
			"content_mode": record.ContentMode, "operational_risk": record.OperationalRisk,
			"metadata": map[string]any{"block_id": record.BlockID,
				"revision_id": record.RevisionID, "status": "candidate"},
		})
	}
	pack := func(budget int) (memory.PackedEvidencePayload, error) {
		return memory.PackEvidencePayload(cards, memory.PackOptions{MaxTokens: budget})
	}
	packed, err := pack(maxTokens)
	if err != nil {
		return WorkingSetRead{}, err
	}
	if len([]byte(packed.RenderedContext)) > maxBytes {
		lower, upper := 0, maxTokens
		packed, err = pack(0)
		if err != nil {
			return WorkingSetRead{}, err
		}
		for lower <= upper {
			middle := (lower + upper) / 2
			candidate, err := pack(middle)
			if err != nil {
				return WorkingSetRead{}, err
			}
			if len([]byte(candidate.RenderedContext)) <= maxBytes {
				packed = candidate
				lower = middle + 1
			} else {
				upper = middle - 1
			}
		}
	}
	visible := map[string]bool{}
	for _, entry := range packed.Visibility {
		if entry.State != "not_visible" {
			visible[entry.RecordID] = true
		}
	}
	for _, id := range selectedIDs {
		if !visible[id] {
			result.OmittedRecordIDs = append(result.OmittedRecordIDs, id)
		}
	}
	if len(packed.Items) > 0 {
		result.RenderedContext = packed.RenderedContext
		result.EstimatedTokens = packed.EstimatedTokens
	}
	result.RenderedBytes = len([]byte(result.RenderedContext))
	result.Truncated = len(result.OmittedRecordIDs) > 0 || packed.TruncatedItemCount > 0
	return result, nil
}
