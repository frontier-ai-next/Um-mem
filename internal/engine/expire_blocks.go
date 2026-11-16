package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/catalog"
)

func parseBlockExpiry(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("timestamp %q must be ISO-8601", value)
}

func lifecycleExpiry(state catalog.BlockState) (time.Time, bool, error) {
	raw := strings.TrimSpace(stringValue(state.Block.LifecyclePolicy["expires_at"]))
	if raw == "" {
		return time.Time{}, false, nil
	}
	expiry, err := parseBlockExpiry(raw)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("memory block %q expiry: %w", state.Block.BlockID, err)
	}
	return expiry, true, nil
}

// ExpireDueBlocks is an opt-in maintenance pass. It archives only explicitly
// expired active blocks whose children, graph, masks and current records have
// been checked. Empty blocks without an expiry remain untouched.
func (engine *Engine) ExpireDueBlocks(ctx context.Context, asOf, reason string) (map[string]any, error) {
	now := time.Now().UTC()
	if strings.TrimSpace(asOf) != "" {
		var err error
		now, err = parseBlockExpiry(asOf)
		if err != nil {
			return nil, fmt.Errorf("as_of must be ISO-8601: %w", err)
		}
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "memory block lifecycle TTL elapsed"
	}
	opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	states, err := opened.ListBlocks(ctx, false)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]catalog.BlockState, len(states))
	due := make([]catalog.BlockState, 0)
	for _, state := range states {
		byID[state.Block.BlockID] = state
		expiry, exists, err := lifecycleExpiry(state)
		if err != nil {
			return nil, err
		}
		if exists && !expiry.After(now) {
			due = append(due, state)
		}
	}
	depth := func(state catalog.BlockState) int {
		count := 0
		for parent := state.Block.ParentID; parent != ""; {
			ancestor, exists := byID[parent]
			if !exists || count > len(byID) {
				break
			}
			count++
			parent = ancestor.Block.ParentID
		}
		return count
	}
	slices.SortStableFunc(due, func(left, right catalog.BlockState) int {
		return depth(right) - depth(left)
	})
	archived := make([]string, 0)
	blockers := make(map[string][]string)
	for _, candidate := range due {
		current, err := opened.GetBlock(ctx, candidate.Block.BlockID)
		if err != nil {
			return nil, err
		}
		if current == nil || current.Status != "active" {
			continue
		}
		expiry, exists, err := lifecycleExpiry(*current)
		if err != nil {
			return nil, err
		}
		if !exists || expiry.After(now) {
			continue
		}
		latest, err := opened.ListBlocks(ctx, false)
		if err != nil {
			return nil, err
		}
		reasons := make([]string, 0, 4)
		for _, child := range latest {
			if child.Block.ParentID == current.Block.BlockID {
				reasons = append(reasons, "block_has_children")
				break
			}
		}
		edges, err := opened.PageEdges(ctx, current.Block.BlockID, 1, "", nil)
		if err != nil {
			return nil, err
		}
		if edges.Total > 0 {
			reasons = append(reasons, "block_has_graph_edges")
		}
		masked, err := opened.BlockHasActiveMaskReference(ctx, current.Block.BlockID)
		if err != nil {
			return nil, err
		}
		if masked {
			reasons = append(reasons, "block_has_active_mask_references")
		}
		if len(reasons) > 0 {
			blockers[current.Block.BlockID] = reasons
			continue
		}
		currentIDs, err := engine.index.RecordIDsForLogicalBlocks(ctx, []string{current.Block.BlockID})
		if err != nil {
			blockers[current.Block.BlockID] = []string{"record_coverage_unavailable:" + err.Error()}
			continue
		}
		rows, pending, err := engine.currentRecordsInBlock(ctx, current.Block.BlockID)
		if err != nil {
			blockers[current.Block.BlockID] = []string{"record_coverage_unavailable:" + err.Error()}
			continue
		}
		if pending {
			blockers[current.Block.BlockID] = []string{"record_coverage_unavailable:pending native write"}
			continue
		}
		if len(currentIDs) > 0 || len(rows) > 0 {
			blockers[current.Block.BlockID] = []string{"block_has_current_records"}
			continue
		}
		if current.Protected {
			sessionID := strings.TrimSpace(stringValue(current.Block.Metadata["umbrella_memory_session_id"]))
			if current.Block.Scope != "session" || sessionID == "" {
				blockers[current.Block.BlockID] = []string{"block_is_protected"}
				continue
			}
			_, err = engine.ArchiveSessionBlock(ctx, SessionBlockRequest{BlockID: current.Block.BlockID,
				ExpectedRevision: current.Revision, SessionID: sessionID, Reason: reason})
		} else {
			_, err = engine.ArchiveBlock(ctx, BlockTransitionRequest{BlockID: current.Block.BlockID,
				ExpectedRevision: current.Revision, Reason: reason})
		}
		if err != nil {
			return nil, err
		}
		archived = append(archived, current.Block.BlockID)
	}
	return map[string]any{"ok": true, "archived_block_ids": archived, "blockers": blockers,
		"as_of": now.Format("2006-01-02T15:04:05.999999999-07:00")}, nil
}
