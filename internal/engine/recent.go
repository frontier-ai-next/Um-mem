package engine

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type RecentRequest struct {
	Limit             int
	Since             string
	Scope             string
	ScopeID           string
	LogicalBlockID    string
	IncludeCandidates bool
}

func recentTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

// Recent returns current, committed records in one exact scope, newest first.
// It reads the canonical projection journal, so an interrupted native write
// cannot appear as a recent record.
func (engine *Engine) Recent(ctx context.Context, request RecentRequest) ([]memory.MemoryRecord, error) {
	if request.Limit < 1 || request.Limit > 128 {
		return nil, errors.New("recent_records limit must be 1-128")
	}
	if request.Scope == "" {
		request.Scope = "workspace"
	}
	if request.Scope != "workspace" && request.Scope != "project" && request.Scope != "session" &&
		request.Scope != "task" && request.Scope != "subtask" && request.Scope != "user_global" {
		return nil, errors.New("recent_records requires a typed scope")
	}
	if request.ScopeID == "" && request.Scope == "workspace" {
		request.ScopeID = engine.config.WorkspaceID
	}
	if request.ScopeID == "" || request.ScopeID != strings.TrimSpace(request.ScopeID) {
		return nil, errors.New("recent_records requires an exact scope_id")
	}
	if request.LogicalBlockID != strings.TrimSpace(request.LogicalBlockID) {
		return nil, errors.New("recent_records requires an exact logical_block_id")
	}
	var cutoff time.Time
	if strings.TrimSpace(request.Since) != "" {
		var ok bool
		cutoff, ok = recentTime(request.Since)
		if !ok {
			return nil, errors.New("recent since must be an ISO-8601 timestamp")
		}
	}
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return nil, err
	}
	scopeKey := request.Scope + ":" + request.ScopeID
	records := make([]memory.MemoryRecord, 0)
	for _, row := range rows {
		record := row.Record
		if !row.Current || row.Status != "committed" || row.Backend != engine.recordNativeStore(row.Record) ||
			record.DeliveryPolicy == "hidden" || record.BlockMembership == nil ||
			record.Scope != request.Scope || !request.IncludeCandidates && record.Status == "candidate" {
			continue
		}
		key, err := record.ScopeKey()
		if err != nil || key != scopeKey {
			continue
		}
		if request.LogicalBlockID != "" && record.BlockMembership.PrimaryBlockID != request.LogicalBlockID {
			continue
		}
		recordedAt, valid := recentTime(record.RecordedAt)
		if !cutoff.IsZero() && (!valid || recordedAt.Before(cutoff)) {
			continue
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		left, _ := recentTime(records[i].RecordedAt)
		right, _ := recentTime(records[j].RecordedAt)
		if !left.Equal(right) {
			return left.After(right)
		}
		return records[i].BlockID > records[j].BlockID
	})
	if len(records) > request.Limit {
		records = records[:request.Limit]
	}
	return records, nil
}
