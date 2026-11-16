package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/memory"
)

// commitRawWitnessProvenanceTx binds a newly published root to every raw host
// witness it cites. The current-head check and edge insert share the same
// SQLite write transaction as publication, so retraction cannot interleave.
func (writer *Writer) commitRawWitnessProvenanceTx(ctx context.Context, tx *sql.Tx,
	record memory.MemoryRecord) error {
	seen := make(map[string]string)
	for _, ref := range record.EvidenceRefs {
		if ref.RefType != "artifact" {
			continue
		}
		refJSON, err := jsoncompat.MarshalPython(ref, false)
		if err != nil {
			return err
		}
		if prior, exists := seen[ref.RefID]; exists {
			if prior != string(refJSON) {
				return errors.New("raw witness provenance has conflicting duplicate references")
			}
			continue
		}
		var payload string
		err = tx.QueryRowContext(ctx, `SELECT payload_json FROM blocks
			WHERE workspace_id=? AND block_id=?`, writer.workspaceID, ref.RefID).Scan(&payload)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		witness, err := memory.DecodeCanonicalRecord([]byte(payload))
		if err != nil {
			return err
		}
		if witness.Metadata["record_kind"] != "raw_host_witness" {
			continue
		}
		if witness.ProfileID != record.ProfileID || witness.SessionID != record.SessionID ||
			witness.ProjectID != record.ProjectID || witness.CanonicalKey == "" ||
			witness.Status != "candidate" || ref.RefType != "artifact" ||
			ref.ProducedBy != "watcher" || ref.Hash == nil ||
			*ref.Hash != "sha256:"+sha256Hex(witness.Content) {
			return errors.New("raw witness provenance does not match scope or exact source")
		}
		producer := false
		for _, source := range witness.EvidenceRefs {
			if source.ProducerID != "" && source.ProducerID == ref.ProducerID {
				producer = true
				break
			}
		}
		if !producer {
			return errors.New("raw witness provenance producer is invalid")
		}
		var currentID string
		err = tx.QueryRowContext(ctx, `SELECT block_id FROM canonical_heads
			WHERE workspace_id=? AND canonical_key_sha256=?`, writer.workspaceID,
			sha256Hex(witness.CanonicalKey)).Scan(&currentID)
		if err != nil || currentID != witness.BlockID {
			return errors.New("raw witness registration is no longer current")
		}
		edge := memory.EdgeRecord{SrcID: record.BlockID, DstID: witness.BlockID,
			EdgeType: "derived_from", WorkspaceID: writer.workspaceID,
			RunID: record.SessionID, SubtaskID: record.SubtaskID,
			Reason: "agent_authored_from_raw_witness", Weight: json.Number("1.0"),
			EvidenceRefs: []memory.EvidenceRef{ref},
			Metadata:     map[string]any{"source_kind": "raw_host_witness"},
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
		seen[ref.RefID] = string(refJSON)
	}
	return nil
}
