package engine

import (
	"context"
	"errors"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func validRawWitnessArtifactRef(ref memory.EvidenceRef, witness memory.MemoryRecord) bool {
	if ref.RefType != "artifact" || ref.RefID != witness.BlockID ||
		ref.ProducedBy != "watcher" || ref.Hash == nil ||
		*ref.Hash != "sha256:"+sha256Text(witness.Content) {
		return false
	}
	for _, source := range witness.EvidenceRefs {
		if source.ProducerID != "" && source.ProducerID == ref.ProducerID {
			return true
		}
	}
	return false
}

func (engine *Engine) requireCurrentRawWitness(ctx context.Context, witness memory.MemoryRecord) error {
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Backend == engine.recordNativeStore(row.Record) && row.Status == "committed" &&
			row.Current && row.Record.CanonicalKey == witness.CanonicalKey {
			if row.Record.BlockID == witness.BlockID && row.Record.Status == "candidate" {
				return nil
			}
			return errors.New("raw witness registration is no longer current")
		}
	}
	return errors.New("raw witness registration has no committed current head")
}

func (engine *Engine) validateRawWitnessEvidence(ctx context.Context, refs []memory.EvidenceRef,
	sessionID, projectID string) ([]string, error) {
	ids := make([]string, 0)
	seen := make(map[string]bool)
	for _, ref := range refs {
		witness, err := engine.ReadStored(ctx, ref.RefID)
		if err != nil {
			return nil, err
		}
		if witness != nil && witness.Metadata["record_kind"] == "raw_host_witness" {
			if !validRawWitnessArtifactRef(ref, *witness) ||
				witness.SessionID != sessionID || witness.ProjectID != projectID {
				return nil, errors.New("raw witness evidence must bind exact host bytes, producer and scope")
			}
			if err := engine.requireCurrentRawWitness(ctx, *witness); err != nil {
				return nil, err
			}
			if !seen[witness.BlockID] {
				ids = append(ids, witness.BlockID)
				seen[witness.BlockID] = true
			}
		}
	}
	return ids, nil
}

// Publication already inserted these edges in its canonical transaction.
// Build response data without a second fallible operation after commit.
func rawWitnessProvenanceReceipts(recordID string, witnessIDs []string, replayed bool) []map[string]any {
	linked := make([]map[string]any, 0, len(witnessIDs))
	for _, id := range witnessIDs {
		linked = append(linked, map[string]any{"src_id": recordID,
			"dst_id": id, "edge_type": "derived_from", "replayed": replayed})
	}
	return linked
}
