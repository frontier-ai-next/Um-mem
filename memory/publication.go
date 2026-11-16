package memory

import (
	"errors"
	"strings"
)

// PublicationCommitReceipt is the durable, request-bound result of accepting a
// verified candidate. Its JSON shape matches Python's PublicationCommitReceipt.
type PublicationCommitReceipt struct {
	ReceiptID        string `json:"receipt_id"`
	RequestHash      string `json:"request_hash"`
	WorkspaceID      string `json:"workspace_id"`
	ProfileID        string `json:"profile_id"`
	SourceID         string `json:"source_id"`
	SourceRevisionID string `json:"source_revision_id"`
	RecordID         string `json:"record_id"`
	RevisionID       string `json:"revision_id"`
	TargetBlockID    string `json:"target_block_id"`
	TargetScope      string `json:"target_scope"`
	TargetScopeID    string `json:"target_scope_id"`
	Authority        string `json:"authority"`
	MaterializedCopy bool   `json:"materialized_copy"`
}

func (receipt PublicationCommitReceipt) Validate() error {
	for _, value := range []string{receipt.ReceiptID, receipt.RequestHash,
		receipt.WorkspaceID, receipt.ProfileID, receipt.SourceID,
		receipt.SourceRevisionID, receipt.RecordID, receipt.RevisionID,
		receipt.TargetBlockID, receipt.TargetScope, receipt.TargetScopeID,
		receipt.Authority} {
		if value == "" || value != strings.TrimSpace(value) {
			return errors.New("publication receipt requires exact nonempty identities")
		}
	}
	if receipt.Authority != "advisory" && receipt.Authority != "directive" {
		return errors.New("publication receipt authority is invalid")
	}
	return nil
}

func (receipt PublicationCommitReceipt) ValidateForRecord(record MemoryRecord) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	if record.BlockMembership == nil || receipt.WorkspaceID != record.WorkspaceID ||
		receipt.ProfileID != record.ProfileID || receipt.RecordID != record.BlockID ||
		receipt.RevisionID != record.RevisionID ||
		receipt.TargetBlockID != record.BlockMembership.PrimaryBlockID ||
		receipt.TargetScope != record.Scope || receipt.Authority != record.Authority ||
		record.Status != "accepted" || record.VerificationStatus != "verified" ||
		record.PromotionDecision != "accepted" {
		return errors.New("publication receipt does not bind the accepted record")
	}
	scopeKey, err := record.ScopeKey()
	if err != nil || scopeKey != receipt.TargetScope+":"+receipt.TargetScopeID {
		return errors.New("publication receipt target scope differs from accepted record")
	}
	if receipt.MaterializedCopy != (record.RevisionOf == "") ||
		(receipt.MaterializedCopy && receipt.Authority != "advisory") {
		return errors.New("publication receipt copy or successor identity is invalid")
	}
	if record.Metadata["promotion_source_id"] != receipt.SourceID ||
		record.Metadata["promotion_source_revision_id"] != receipt.SourceRevisionID {
		return errors.New("publication receipt source differs from the record provenance")
	}
	return nil
}

func (receipt PublicationCommitReceipt) Response(backend string) map[string]any {
	return map[string]any{
		"ok": true, "saved": true, "block_id": receipt.RecordID,
		"source_memory_id":     receipt.SourceID,
		"result_memory_id":     receipt.RecordID,
		"promotion_receipt_id": receipt.ReceiptID,
		"revision_id":          receipt.RevisionID,
		"requested_block_id":   receipt.TargetBlockID,
		"actual_block_id":      receipt.TargetBlockID,
		"target_scope":         receipt.TargetScope,
		"target_scope_id":      receipt.TargetScopeID,
		"authority":            receipt.Authority,
		"status":               "accepted", "verification_status": "verified",
		"promotion_decision": "accepted", "tribunal_decision": "accept",
		"backend": backend,
	}
}
