package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// RegisterSource is the direct API for exact conversation-turn bytes. It
// shares the host witness writer but never chooses a semantic destination.
func (engine *Engine) RegisterSource(ctx context.Context, sourceID, sourceText,
	producerID, speaker, occurredAt string, lineage HostLineage) (map[string]any, error) {
	if sourceID == "" || sourceID != strings.TrimSpace(sourceID) || len(sourceID) > 256 ||
		strings.ContainsRune(sourceID, 0) || sourceText == "" ||
		producerID == "" || producerID != strings.TrimSpace(producerID) {
		return nil, errors.New("registered source requires bounded source_id, nonempty text and producer_id")
	}
	observedAt := occurredAt
	if observedAt == "" {
		observedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	result, err := engine.ObserveHost(ctx, HostObservation{EventID: sourceID,
		EventKind: "conversation_turn", RawEvidence: sourceText,
		ObservedAt: observedAt, ProducerID: producerID,
		Speaker: speaker, SourceOccurredAt: occurredAt}, lineage)
	if err != nil {
		return nil, err
	}
	return map[string]any{"source_id": sourceID, "source_sha256": result["source_sha256"],
		"source_bytes": result["source_bytes"], "witness_id": result["witness_id"],
		"workspace_id": engine.config.WorkspaceID, "profile_id": engine.config.ProfileID,
		"session_id": lineage.SessionID, "task_id": lineage.TaskID}, nil
}

type sourceInterval struct{ start, end int }

func exactByteOffset(value any) (int, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		return int(parsed), err == nil && parsed >= 0 && parsed <= int64(math.MaxInt)
	case int:
		return typed, typed >= 0
	case int64:
		return int(typed), typed >= 0 && typed <= int64(math.MaxInt)
	case float64:
		// The MCP SDK decodes JSON objects into float64. Only integers that
		// survived that conversion exactly can represent source byte offsets.
		if typed >= 0 && typed < 1<<53 && typed <= float64(math.MaxInt) && math.Trunc(typed) == typed {
			return int(typed), true
		}
		return 0, false
	default:
		return 0, false
	}
}

func coveredBytes(intervals []sourceInterval) int {
	sort.Slice(intervals, func(i, j int) bool {
		if intervals[i].start != intervals[j].start {
			return intervals[i].start < intervals[j].start
		}
		return intervals[i].end < intervals[j].end
	})
	total, end := 0, 0
	for _, interval := range intervals {
		if interval.end > end {
			total += interval.end - max(interval.start, end)
			end = interval.end
		}
	}
	return total
}

// SourceCoverage verifies exact UTF-8 byte slices from committed, current
// lossless atomic records against registered raw host witnesses.
func (engine *Engine) SourceCoverage(ctx context.Context, expectedSourceIDs []string) (map[string]any, error) {
	if len(expectedSourceIDs) == 0 {
		return nil, errors.New("source coverage requires distinct expected source IDs")
	}
	seen := make(map[string]bool)
	for _, id := range expectedSourceIDs {
		if id == "" || seen[id] {
			return nil, errors.New("source coverage requires distinct expected source IDs")
		}
		seen[id] = true
	}
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make([]memory.MemoryRecord, 0)
	for _, row := range rows {
		if row.Backend == engine.recordNativeStore(row.Record) && row.Status == "committed" && row.Current &&
			(row.Record.Status == "candidate" || row.Record.Status == "accepted") &&
			row.Record.Metadata["memory_mode"] == "lossless_atomic" {
			segments, ok := row.Record.Metadata["source_segments"].([]any)
			if !ok || len(segments) == 0 {
				continue
			}
			var fullText strings.Builder
			valid := true
			for _, raw := range segments {
				segment, ok := raw.(map[string]any)
				if !ok {
					valid = false
					break
				}
				text, ok := segment["text"].(string)
				if !ok || text == "" {
					valid = false
					break
				}
				fullText.WriteString(text)
			}
			if valid && fullText.String() == row.Record.Content {
				candidates = append(candidates, row.Record)
			}
		}
	}
	registeredBytes, totalCovered, fullyCovered := 0, 0, 0
	unknown := make([]string, 0)
	coveredRecords := make(map[string]bool)
	perSource := make(map[string]any, len(expectedSourceIDs))
	for _, sourceID := range expectedSourceIDs {
		canonicalKey, err := memory.DeriveCanonicalKey(engine.config.WorkspaceID,
			engine.config.ProfileID, "raw_host_witness", "host:"+sourceID+":raw_host_witness")
		if err != nil {
			return nil, err
		}
		witness, err := engine.ReadStored(ctx, memory.BlockIDForCanonicalKey(canonicalKey))
		if err != nil {
			return nil, err
		}
		if witness == nil || witness.Metadata["record_kind"] != "raw_host_witness" ||
			witness.Metadata["host_event_id"] != sourceID ||
			witness.Metadata["host_event_kind"] != "conversation_turn" {
			unknown = append(unknown, sourceID)
			perSource[sourceID] = map[string]any{"registered": false, "covered_bytes": 0}
			continue
		}
		current := false
		for _, row := range rows {
			if row.Backend == engine.recordNativeStore(row.Record) && row.Status == "committed" &&
				row.Current && row.Record.CanonicalKey == witness.CanonicalKey &&
				row.Record.BlockID == witness.BlockID && row.Record.Status == "candidate" {
				current = true
				break
			}
		}
		if !current {
			unknown = append(unknown, sourceID)
			perSource[sourceID] = map[string]any{"registered": false, "covered_bytes": 0}
			continue
		}
		source := []byte(witness.Content)
		digest := sha256.Sum256(source)
		hash := hex.EncodeToString(digest[:])
		intervals := make([]sourceInterval, 0)
		for _, candidate := range candidates {
			if candidate.SessionID != witness.SessionID || candidate.ProjectID != witness.ProjectID {
				continue
			}
			segments, ok := candidate.Metadata["source_segments"].([]any)
			if !ok {
				continue
			}
			for _, raw := range segments {
				segment, ok := raw.(map[string]any)
				if !ok || segment["source_id"] != sourceID || segment["source_sha256"] != hash ||
					segment["witness_id"] != witness.BlockID {
					continue
				}
				start, startOK := exactByteOffset(segment["byte_start"])
				end, endOK := exactByteOffset(segment["byte_end"])
				text, textOK := segment["text"].(string)
				if !startOK || !endOK || !textOK || start >= end || end > len(source) ||
					string(source[start:end]) != text {
					continue
				}
				intervals = append(intervals, sourceInterval{start, end})
				coveredRecords[candidate.BlockID] = true
			}
		}
		coverage := coveredBytes(intervals)
		registeredBytes += len(source)
		totalCovered += coverage
		if coverage == len(source) {
			fullyCovered++
		}
		perSource[sourceID] = map[string]any{"registered": true,
			"source_bytes": len(source), "covered_bytes": coverage}
	}
	ids := make([]string, 0, len(coveredRecords))
	for id := range coveredRecords {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var sourceBytes any = registeredBytes
	var covered any = totalCovered
	var byteFraction any
	if len(unknown) > 0 {
		sourceBytes, covered = nil, nil
	} else if registeredBytes > 0 {
		byteFraction = float64(totalCovered) / float64(registeredBytes)
	}
	return map[string]any{"expected_source_ids": expectedSourceIDs,
		"unknown_source_ids": unknown, "source_bytes": sourceBytes,
		"covered_bytes": covered, "source_byte_fraction": byteFraction,
		"source_id_fraction": float64(fullyCovered) / float64(len(expectedSourceIDs)),
		"record_ids":         ids, "sources": perSource}, nil
}
