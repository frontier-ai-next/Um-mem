package memory

import (
	"errors"
	"strings"
)

// ConsolidationCommitReceipt is the durable receipt stored with the canonical
// mutation. It is distinct from the transient shadow commit receipt.
type ConsolidationCommitReceipt struct {
	ReceiptID    string        `json:"receipt_id"`
	EvidenceRefs []EvidenceRef `json:"evidence_refs"`
	CommittedIDs []string      `json:"committed_ids"`
}

func (receipt ConsolidationCommitReceipt) ValidateForRecord(record MemoryRecord) error {
	if strings.TrimSpace(receipt.ReceiptID) == "" || len(receipt.EvidenceRefs) == 0 ||
		len(receipt.CommittedIDs) == 0 {
		return errors.New("consolidation commit requires receipt identity, evidence and committed IDs")
	}
	for _, ref := range receipt.EvidenceRefs {
		if err := ref.Validate(); err != nil {
			return err
		}
	}
	matched := false
	seen := map[string]bool{}
	for _, id := range receipt.CommittedIDs {
		if id == "" || id != strings.TrimSpace(id) || seen[id] {
			return errors.New("consolidation committed IDs must be exact and unique")
		}
		seen[id] = true
		matched = matched || id == record.BlockID
	}
	if !matched {
		return errors.New("consolidation receipt must commit the mutation successor")
	}
	return nil
}
