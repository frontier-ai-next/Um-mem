package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// Approve records that a human read the record and vouches for it. It appends a
// `human_approval` evidence ref through the ordinary amendment path: the engine's
// tribunal stays the only way to directive authority, and this is the honest
// human-in-the-loop signal a promotion review can act on.
func (engine *Engine) Approve(ctx context.Context, recordID, approver, reason string) (map[string]any, error) {
	id := strings.TrimSpace(recordID)
	if id == "" {
		return nil, errors.New("approve requires a record id")
	}
	who := strings.TrimSpace(approver)
	if who == "" {
		who = "operator"
	}
	note := strings.TrimSpace(reason)
	if note == "" {
		note = "human approval"
	}
	record, err := engine.ReadStored(ctx, id)
	if err != nil {
		return nil, err
	}
	refs := append([]memory.EvidenceRef{}, record.EvidenceRefs...)
	for _, ref := range refs {
		if ref.RefType == "human_approval" && ref.ProducerID == who {
			return map[string]any{"ok": true, "approved": false, "reason": "already approved by this operator",
				"record_id": id}, nil
		}
	}
	refs = append(refs, memory.EvidenceRef{
		RefType: "human_approval", RefID: "approval:" + id + ":" + who,
		ProducedBy: "human", ProducerID: who,
	})
	payload, err := engine.Amend(ctx, AmendRequest{
		MemoryID:         id,
		ExpectedRevision: record.RevisionID,
		Reason:           note,
		EvidenceRefs:     refs,
		OperationID:      "approve:" + id + ":" + who,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "approved": true, "record_id": id, "approver": who,
		"evidence_count": len(refs), "amend": payload}, nil
}
