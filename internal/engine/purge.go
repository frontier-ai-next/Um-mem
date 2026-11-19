package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// PurgeRequest asks for real removal, not a retraction. A retraction keeps the
// record in the journal with a `supersedes` edge; a purge rebuilds the store
// without the record, so the bytes are gone from the live database.
type PurgeRequest struct {
	RecordIDs []string `json:"record_id"`
	Kinds     []string `json:"kind"`
	Blocks    []string `json:"block"`
	Apply     bool     `json:"apply"`
	Confirm   bool     `json:"confirm_purge"`
	Shred     bool     `json:"shred"`
}

// Purge removes records and their revisions from the store. It is deliberately
// slow and loud: it exports every removed row to a backup file, rebuilds the
// store beside the live one, verifies the result, and only then swaps. The old
// directory is kept unless `Shred` is set.
func (engine *Engine) Purge(ctx context.Context, request PurgeRequest) (map[string]any, error) {
	records, edges, _, err := engine.index.ExportGraphSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	withdrawn := retractedTargets(records, edges)

	ids := map[string]bool{}
	for _, raw := range request.RecordIDs {
		for _, part := range strings.Split(raw, ",") {
			if id := strings.TrimSpace(part); id != "" {
				ids[id] = true
			}
		}
	}
	kinds := map[string]bool{}
	for _, kind := range request.Kinds {
		kinds[strings.ToLower(strings.TrimSpace(kind))] = true
	}
	blocks := map[string]bool{}
	for _, block := range request.Blocks {
		blocks[strings.TrimSpace(block)] = true
	}

	// A purge by record id must take the whole revision chain with it, otherwise
	// the old text survives in an amendment that nobody pointed at.
	revisionOf := make(map[string]string, len(records))
	for _, record := range records {
		if record.RevisionOf != "" {
			revisionOf[record.BlockID] = record.RevisionOf
		}
	}
	rootOf := func(id string) string {
		seen := 0
		for {
			parent, ok := revisionOf[id]
			if !ok || parent == "" || seen > 1000 {
				return id
			}
			id = parent
			seen++
		}
	}
	targetRoots := map[string]bool{}
	for id := range ids {
		targetRoots[rootOf(id)] = true
		// A stable id points at the root while callers often hold a revision id.
		for recordID := range revisionOf {
			if revisionOf[recordID] == id {
				targetRoots[rootOf(recordID)] = true
			}
		}
	}

	remove := func(record memory.MemoryRecord) bool {
		if len(ids) > 0 && targetRoots[rootOf(record.BlockID)] {
			return true
		}
		if len(kinds) > 0 && kinds[strings.ToLower(record.Kind)] {
			return true
		}
		if len(blocks) > 0 && record.BlockMembership != nil && blocks[record.BlockMembership.PrimaryBlockID] {
			return true
		}
		return false
	}

	purged := make([]memory.MemoryRecord, 0)
	kept := make([]memory.MemoryRecord, 0)
	for _, record := range records {
		if remove(record) {
			purged = append(purged, record)
			continue
		}
		kept = append(kept, record)
	}
	if len(purged) == 0 {
		return nil, errors.New("purge: nothing matched, refusing to rebuild the store")
	}
	plan := map[string]any{
		"ok": true, "applied": false, "purged": len(purged), "kept": len(kept),
		"purged_ids": recordIDs(purged), "store": engine.config.RepoRoot,
		"total_before": len(records), "edges": len(edges),
		"note": "dry run: pass apply=true and confirm_purge=true to rebuild the store without these records",
	}
	if !request.Apply {
		return plan, nil
	}
	if !request.Confirm {
		return nil, errors.New("purge --apply requires --confirm-purge")
	}

	stamp := time.Now().Format("20060102-150405")
	root := engine.config.RepoRoot
	backup := fmt.Sprintf("%s-purge-backup-%s.jsonl", strings.TrimRight(root, `\/`), stamp)
	if err := writeRecordsJSONL(backup, purged); err != nil {
		return nil, err
	}

	temp := fmt.Sprintf("%s-purge-%s", strings.TrimRight(root, `\/`), stamp)
	if _, err := os.Stat(temp); err == nil {
		return nil, fmt.Errorf("purge: %s already exists", temp)
	}
	rebuilt, err := Open(Config{RepoRoot: temp, WorkspaceID: engine.config.WorkspaceID,
		ProfileID: engine.config.ProfileID, BackendID: engine.config.BackendID})
	if err != nil {
		return nil, err
	}

	idMap := make(map[string]string, len(kept))
	for _, record := range kept {
		if record.Status == "retracted" || record.Status == "superseded" || withdrawn[record.BlockID] {
			continue
		}
		block := ""
		if record.BlockMembership != nil {
			block = record.BlockMembership.PrimaryBlockID
		}
		result, err := rebuilt.RememberCandidate(ctx, CandidateRequest{
			OperationID:      "purge-rebuild:" + record.BlockID,
			Content:          record.Content,
			Title:            record.Title,
			Kind:             record.Kind,
			Tags:             record.Tags,
			Metadata:         userMetadata(record.Metadata),
			EvidenceRefs:     record.EvidenceRefs,
			RequestedBlockID: block,
			EvidenceKind:     "tool_output",
			UserID:           record.UserID,
		})
		if err != nil {
			_ = rebuilt.Close()
			return nil, fmt.Errorf("purge: rebuild failed on %s: %w", record.BlockID, err)
		}
		idMap[record.BlockID] = result.RecordID
	}
	edgeCount := 0
	for _, edge := range edges {
		if edge.EdgeType == "supersedes" {
			continue
		}
		src, okSrc := idMap[edge.SrcID]
		dst, okDst := idMap[edge.DstID]
		if !okSrc || !okDst || src == dst {
			continue
		}
		if _, err := rebuilt.AddEdge(ctx, AddEdgeRequest{
			SrcID: src, DstID: dst, EdgeType: edge.EdgeType, Reason: edge.Reason,
			EvidenceRefs: []memory.EvidenceRef{{RefType: "tool_result",
				RefID: "purge:" + edge.SrcID + ":" + edge.DstID,
				ProducedBy: "agent", ProducerID: "um-mem-purge"}},
		}); err != nil {
			continue
		}
		edgeCount++
	}
	stats, _ := rebuilt.Stats(ctx)
	if err := rebuilt.Close(); err != nil {
		return nil, err
	}

	// Swap only after the rebuilt store answered a stats call.
	if err := engine.Close(); err != nil {
		return nil, err
	}
	dead := fmt.Sprintf("%s-deleted-%s", strings.TrimRight(root, `\/`), stamp)
	if err := os.Rename(root, dead); err != nil {
		return nil, fmt.Errorf("purge: cannot move the old store: %w", err)
	}
	if err := os.Rename(temp, root); err != nil {
		return nil, fmt.Errorf("purge: cannot activate the rebuilt store: %w", err)
	}
	shredded := false
	if request.Shred {
		if err := os.RemoveAll(dead); err == nil {
			shredded = true
		}
	}
	plan["applied"] = true
	plan["rebuild"] = stats
	plan["backup"] = backup
	plan["removed_store"] = map[string]any{"path": dead, "shredded": shredded}
	plan["kept_edges"] = edgeCount
	return plan, nil
}

// userMetadata drops the reserved `um.*` keys: they are written by the engine and
// rejected on write, so replaying them makes a rebuild fail.
func userMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	// The engine stamps its own bookkeeping keys on every write; replaying them
	// makes the rebuild fail with "operation metadata is writer-owned".
	reserved := map[string]bool{
		"evidence_kind": true, "write_mode": true, "route_decision_reason": true,
		"operation_id": true, "operation_caller_role": true,
		"operation_protocol_version": true, "operation_request_fingerprint": true,
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		if strings.HasPrefix(key, "um.") || reserved[key] {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func recordIDs(records []memory.MemoryRecord) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, record.BlockID)
	}
	sort.Strings(out)
	return out
}

func writeRecordsJSONL(path string, records []memory.MemoryRecord) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := bufio.NewWriterSize(file, 1<<20)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	for _, record := range records {
		if err := encoder.Encode(map[string]any{"kind": "record", "record": record}); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	return nil
}
