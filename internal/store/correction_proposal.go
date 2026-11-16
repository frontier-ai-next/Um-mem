package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// commitCorrectionProposalTx links an advisory proposal to the exact trusted
// head it challenges before that proposal becomes visible. Old source evidence
// stays historical evidence; publishing a proposal cannot re-register it as a
// fresh same-session raw host witness.
func (writer *Writer) commitCorrectionProposalTx(ctx context.Context, tx *sql.Tx,
	record memory.MemoryRecord) error {
	targetID, idOK := record.Metadata["correction_target_id"].(string)
	targetRevision, revisionOK := record.Metadata["correction_target_revision_id"].(string)
	if !idOK || !revisionOK || targetID == "" || targetRevision == "" ||
		targetID != strings.TrimSpace(targetID) || record.Status != "candidate" ||
		record.Authority != "advisory" || record.TrustLevel != "agent_reported" ||
		record.VerificationStatus != "unverified" || record.BlockMembership == nil {
		return errors.New("correction proposal requires an advisory candidate and exact target identity")
	}
	owner := record.Metadata["working_set_owner"]
	if owner != nil {
		claimed, ok := owner.(map[string]any)
		if !ok || len(claimed) != 2 {
			return errors.New("correction proposal owner must be exact")
		}
		for _, field := range []string{"owner_id", "generation_id"} {
			selected, ok := claimed[field].(string)
			if !ok || strings.TrimSpace(selected) == "" {
				return errors.New("correction proposal owner must be exact")
			}
		}
	}
	var targetJSON string
	if err := tx.QueryRowContext(ctx, `SELECT payload_json FROM blocks
		WHERE workspace_id=? AND block_id=? AND committed_at<>''`,
		writer.workspaceID, targetID).Scan(&targetJSON); err != nil {
		return errors.New("correction proposal target is not committed")
	}
	target, err := memory.DecodeCanonicalRecord([]byte(targetJSON))
	if err != nil {
		return err
	}
	if target.ProfileID != writer.profileID || target.RevisionID != targetRevision ||
		target.CanonicalKey == "" || target.BlockMembership == nil ||
		target.BlockMembership.PrimaryBlockID != record.BlockMembership.PrimaryBlockID ||
		target.Scope != record.Scope ||
		!(target.Status == "accepted" || target.Authority == "directive" ||
			target.TrustLevel == "workspace_verified" || target.TrustLevel == "external_verified" ||
			target.TrustLevel == "supervisor_verified" || target.TrustLevel == "human_approved" ||
			target.TrustLevel == "system_curated") {
		return errors.New("correction proposal target is not the selected trusted memory")
	}
	var currentID string
	if err := tx.QueryRowContext(ctx, `SELECT block_id FROM canonical_heads
		WHERE workspace_id=? AND canonical_key_sha256=?`, writer.workspaceID,
		sha256Hex(target.CanonicalKey)).Scan(&currentID); err != nil || currentID != targetID {
		return errors.New("correction proposal target changed before publication")
	}
	hash, err := memory.PythonHashValue(map[string]any{
		"target_id": targetID, "owner": owner, "session_id": record.SessionID})
	if err != nil {
		return err
	}
	if record.CanonicalKey != target.CanonicalKey+":correction-proposal:"+hash[:16] ||
		record.BlockID != memory.BlockIDForCanonicalKey(record.CanonicalKey) {
		return errors.New("correction proposal identity does not match its target and owner")
	}
	edge := memory.EdgeRecord{SrcID: record.BlockID, DstID: targetID,
		EdgeType: "contradicts", WorkspaceID: writer.workspaceID,
		Reason: "pending_host_correction", Weight: json.Number("1.0"),
		EvidenceRefs: record.EvidenceRefs,
		Metadata:     map[string]any{"correction_target_revision_id": targetRevision},
		CreatedAt:    time.Now().UTC().Format(time.RFC3339)}
	encoded, err := jsoncompat.MarshalPython(edge, false)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO edges(src_id,dst_id,edge_type,workspace_id,payload_json)
		VALUES(?,?,?,?,?)`, edge.SrcID, edge.DstID, edge.EdgeType,
		writer.workspaceID, string(encoded)); err != nil {
		return err
	}
	return nil
}
