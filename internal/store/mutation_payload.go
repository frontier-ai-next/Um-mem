package store

import (
	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/memory"
)

// Python's canonical mutation payload is a sorted, compact JSON object. The
// record payload identity is content-addressed separately.
func canonicalMutationJSON(recordPayloadID, recordChecksum, action, predecessorID, rollbackTargetID string, edge any) (string, error) {
	return canonicalMutationJSONWithPublication(recordPayloadID, recordChecksum,
		action, predecessorID, rollbackTargetID, edge, nil)
}

func canonicalMutationJSONWithPublication(recordPayloadID, recordChecksum, action, predecessorID, rollbackTargetID string, edge any, publication *memory.PublicationCommitReceipt) (string, error) {
	return canonicalMutationJSONWithReceipts(recordPayloadID, recordChecksum, action,
		predecessorID, rollbackTargetID, edge, nil, publication)
}

func canonicalMutationJSONWithReceipts(recordPayloadID, recordChecksum, action, predecessorID, rollbackTargetID string,
	edge any, consolidation *memory.ConsolidationCommitReceipt, publication *memory.PublicationCommitReceipt) (string, error) {
	fields := map[string]any{
		"record_payload_id":     recordPayloadID,
		"record_payload_sha256": recordChecksum,
		"superseded_block_id":   predecessorID,
		"action":                action,
		"edge":                  edge,
		"rollback_target_id":    rollbackTargetID,
		"affected_view_ids":     []string{},
		"consolidation_receipt": consolidation,
		"publication_receipt":   publication,
		"amendment_receipt":     nil,
	}
	encoded, err := jsoncompat.MarshalPython(fields, false)
	return string(encoded), err
}
