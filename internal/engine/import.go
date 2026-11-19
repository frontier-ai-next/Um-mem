package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// ImportJSONL replays an export into a store. Records become candidates again
// (withdrawn history is not resurrected, and directive authority is never
// implied by a file), semantic edges are recreated with the same type, and
// `supersedes` edges are skipped: the export keeps them for audit, but replaying
// them would mark half the imported records as retired.
func (engine *Engine) ImportJSONL(ctx context.Context, path string) (map[string]any, error) {
	target := strings.TrimSpace(path)
	if target == "" {
		return nil, errors.New("import requires an input path")
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	idMap := map[string]string{}
	records, edges, skipped, failed := 0, 0, 0, 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var envelope struct {
			Kind      string            `json:"kind"`
			Withdrawn bool              `json:"withdrawn"`
			Record    memory.MemoryRecord `json:"record"`
			Edge   struct {
				SrcID    string `json:"src_id"`
				DstID    string `json:"dst_id"`
				EdgeType string `json:"edge_type"`
				Reason   string `json:"reason"`
			} `json:"edge"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			failed++
			continue
		}
		// Replaying an audit export must not resurrect retired revisions: they
		// would return as live candidates and pollute recall with duplicates.
		retired := envelope.Record.Status == "retracted" || envelope.Record.Status == "superseded" || envelope.Withdrawn
		if envelope.Kind == "record" && retired {
			skipped++
			continue
		}
		if envelope.Kind == "record" && strings.TrimSpace(envelope.Record.Content) != "" {
			block := envelope.Record.BlockID
			if envelope.Record.BlockMembership != nil {
				block = envelope.Record.BlockMembership.PrimaryBlockID
			}
			result, err := engine.RememberCandidate(ctx, CandidateRequest{
				Content:          envelope.Record.Content,
				Title:            envelope.Record.Title,
				Kind:             envelope.Record.Kind,
				Tags:             envelope.Record.Tags,
				RequestedBlockID: block,
				EvidenceKind:     "tool_output",
				UserID:           envelope.Record.UserID,
			})
			if err != nil {
				failed++
				continue
			}
			records++
			idMap[envelope.Record.BlockID] = result.RecordID
			continue
		}
		if envelope.Kind == "edge" && envelope.Edge.EdgeType != "supersedes" {
			src, srcOK := idMap[envelope.Edge.SrcID]
			dst, dstOK := idMap[envelope.Edge.DstID]
			if !srcOK || !dstOK || src == dst {
				skipped++
				continue
			}
			edge := memory.EdgeRecord{
				SrcID: src, DstID: dst,
				EdgeType: envelope.Edge.EdgeType, WorkspaceID: engine.config.WorkspaceID,
				Reason: envelope.Edge.Reason,
				EvidenceRefs: []memory.EvidenceRef{{
					RefType: "artifact", RefID: "import:" + filepath.Base(target),
					ProducedBy: "agent", ProducerID: "um-mem-import",
				}},
			}
			if _, err := engine.writer.AddEdge(ctx, edge); err != nil {
				skipped++
				continue
			}
			edges++
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "path": target, "records": records, "edges": edges,
		"skipped_edges": skipped, "failed": failed,
		"workspace_id": engine.config.WorkspaceID, "backend": engine.config.BackendID,
		"note": "records import as candidates; directive authority is never implied",
	}, nil
}
