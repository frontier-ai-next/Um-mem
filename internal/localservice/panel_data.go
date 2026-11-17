package localservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func blockTotal(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func panelSummary(record memory.MemoryRecord) map[string]any {
	blockID := ""
	if record.BlockMembership != nil {
		blockID = record.BlockMembership.PrimaryBlockID
	}
	preview := []rune(record.Content)
	if len(preview) > 240 {
		preview = preview[:240]
	}
	labeled := false
	for key := range record.Metadata {
		if !strings.HasPrefix(key, "um.") {
			labeled = true
			break
		}
	}
	return map[string]any{"record_id": record.BlockID, "revision_id": record.RevisionID,
		"revision_of": record.RevisionOf, "title": record.Title, "preview": string(preview),
		"kind": record.Kind, "status": record.Status, "recorded_at": record.RecordedAt,
		"logical_block_id": blockID, "tags": record.Tags, "authority": record.Authority,
		"labeled": labeled}
}

func panelScope(identity Identity, scope, scopeID string) (string, string, error) {
	if scope == "" {
		scope = "workspace"
	}
	if scope != "workspace" && scope != "project" {
		return "", "", errors.New("Panel record scope must be workspace or project")
	}
	if scope == "workspace" {
		if scopeID != "" && scopeID != identity.WorkspaceID {
			return "", "", errors.New("Workspace scope ID does not match this panel")
		}
		return scope, identity.WorkspaceID, nil
	}
	if scopeID == "" || scopeID != strings.TrimSpace(scopeID) {
		return "", "", errors.New("An exact project scope ID is required")
	}
	return scope, scopeID, nil
}

func (panel *Panel) snapshot(ctx context.Context, server *Server, scope, scopeID string) (map[string]any, error) {
	scope, scopeID, err := panelScope(server.identity, scope, scopeID)
	if err != nil {
		return nil, err
	}
	// Capture the cursor before database reads so a concurrent change can be
	// replayed even when the snapshot happens to observe it.
	panel.mu.Lock()
	feed := panel.feed
	panel.mu.Unlock()
	cursor := feed.Cursor()
	catalog, err := panel.selected.PanelCatalog(ctx, scope, scopeID)
	if err != nil {
		return nil, err
	}
	blocks := catalog["blocks"].([]map[string]any)
	byID := make(map[string]map[string]any, len(blocks))
	for _, block := range blocks {
		id := block["block_id"].(string)
		block["children"] = []map[string]any{}
		byID[id] = block
	}
	tree := make([]map[string]any, 0)
	for _, block := range blocks {
		parentID, _ := block["parent_id"].(string)
		if parent, exists := byID[parentID]; exists {
			parent["children"] = append(parent["children"].([]map[string]any), block)
		} else {
			tree = append(tree, block)
		}
	}
	records, err := panel.selected.Recent(ctx, engine.RecentRequest{Limit: 64,
		Scope: scope, ScopeID: scopeID, IncludeCandidates: true})
	if err != nil {
		return nil, err
	}
	edges, edgesTruncated, err := panel.selected.PanelRecordEdges(ctx, records)
	if err != nil {
		return nil, err
	}
	summaries := make([]map[string]any, 0, len(records))
	blockOfRecord := make(map[string]string, len(records))
	labeledOfBlock := map[string]int{}
	countOfBlock := map[string]int{}
	for _, record := range records {
		summary := panelSummary(record)
		summaries = append(summaries, summary)
		block, _ := summary["logical_block_id"].(string)
		if block == "" {
			continue
		}
		blockOfRecord[record.BlockID] = block
		countOfBlock[block]++
		if labeled, _ := summary["labeled"].(bool); labeled {
			labeledOfBlock[block]++
		}
	}
	// The panel drew only a tree because nothing ever put record relations on the
	// wire: aggregate them to block pairs and append those edges to the graph.
	type blockPair struct{ from, to string }
	pairs := map[blockPair]int{}
	for _, edge := range edges {
		src, _ := edge["src_id"].(string)
		dst, _ := edge["dst_id"].(string)
		from, okFrom := blockOfRecord[src]
		to, okTo := blockOfRecord[dst]
		if !okFrom || !okTo || from == to {
			continue
		}
		key := blockPair{from: from, to: to}
		if key.from > key.to {
			key = blockPair{from: to, to: from}
		}
		pairs[key]++
	}
	blockEdges := make([]map[string]any, 0, len(pairs))
	for pair, count := range pairs {
		label := "связь"
		if count > 1 {
			label = fmt.Sprintf("%d связи", count)
		}
		blockEdges = append(blockEdges, map[string]any{"src_id": pair.from, "dst_id": pair.to,
			"relation": label, "edge_type": "related_to", "weight": count})
	}
	relationCount := map[string]int{}
	for pair, count := range pairs {
		relationCount[pair.from] += count
		relationCount[pair.to] += count
	}
	kindOfBlock := map[string]map[string]int{}
	for _, summary := range summaries {
		block, _ := summary["logical_block_id"].(string)
		kind, _ := summary["kind"].(string)
		if block == "" || kind == "" {
			continue
		}
		if kindOfBlock[block] == nil {
			kindOfBlock[block] = map[string]int{}
		}
		kindOfBlock[block][kind]++
	}
	for _, block := range blocks {
		id, _ := block["block_id"].(string)
		if id == "" {
			continue
		}
		block["relation_count"] = relationCount[id]
		if total := countOfBlock[id]; total > 0 {
			block["label_share"] = float64(labeledOfBlock[id]) / float64(total)
		}
		// Blocks carry governance labels (retention, temperature, authority) and a
		// mix of record kinds; the panel showed none of them, so a block looked like
		// a bare folder instead of "research that lives long".
		chips := make([]string, 0, 6)
		if retention, ok := block["retention"].(string); ok && retention != "" {
			chips = append(chips, retention)
		}
		if temperature, ok := block["temperature"].(string); ok && temperature != "" {
			chips = append(chips, temperature)
		}
		if trust, ok := block["trust"].(string); ok && trust != "" {
			chips = append(chips, trust)
		}
		if blockType, ok := block["block_type"].(string); ok && blockType != "" && blockType != "semantic_leaf" {
			chips = append(chips, blockType)
		}
		if kinds := kindOfBlock[id]; len(kinds) > 0 {
			type pair struct {
				name  string
				count int
			}
			ordered := make([]pair, 0, len(kinds))
			for name, count := range kinds {
				ordered = append(ordered, pair{name: name, count: count})
			}
			sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].count > ordered[j].count })
			top := make([]string, 0, 2)
			for index, item := range ordered {
				if index == 2 {
					break
				}
				top = append(top, fmt.Sprintf("%s×%d", item.name, item.count))
			}
			chips = append(chips, strings.Join(top, ", "))
		}
		block["meta"] = chips
	}
	// The catalog tree is a routing taxonomy: its nodes carry no records, so the
	// panel showed a tree of empty boxes. Attach the logical blocks that records
	// actually belong to, under the first visible root, with real counts.
	if len(tree) > 0 {
		root := tree[0]
		rootID, _ := root["block_id"].(string)
		existing, _ := root["children"].([]map[string]any)
		added := 0
		for blockID, total := range countOfBlock {
			if _, exists := byID[blockID]; exists || total == 0 {
				continue
			}
			node := map[string]any{"block_id": blockID, "name": blockID, "parent_id": rootID,
				"status": "active", "index_only": false, "children": []map[string]any{},
				"canonical_record_count": total, "record_count": total, "logical": true,
				"label_share": float64(labeledOfBlock[blockID]) / float64(total)}
			existing = append(existing, node)
			added++
		}
		root["children"] = existing
		if added > 0 {
			root["canonical_record_count"] = blockTotal(countOfBlock)
		}
	}
	merged := make([]map[string]any, 0, len(blockEdges)+16)
	if existing, ok := catalog["edges"].([]map[string]any); ok {
		merged = append(merged, existing...)
	}
	for _, blockEdge := range blockEdges {
		merged = append(merged, blockEdge)
	}
	catalog["edges"] = merged
	server.mu.Lock()
	bySession := make(map[string]Lineage, len(server.leases))
	for token, entry := range server.leases {
		if entry.expires.After(server.now()) {
			bySession[entry.sessionID] = entry.lineage
		} else {
			delete(server.leases, token)
		}
	}
	server.mu.Unlock()
	ids := make([]string, 0, len(bySession))
	for id := range bySession {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	sessions := make([]Lineage, 0, len(ids))
	for _, id := range ids {
		sessions = append(sessions, bySession[id])
	}
	events := feed.Snapshot()
	if events == nil {
		events = []ChangeEvent{}
	}
	return map[string]any{"ok": true, "cursor": cursor, "tree": tree,
		"edges": catalog["edges"], "block_edges_truncated": catalog["edges_truncated"],
		"block_edge_limit": 128, "records": summaries, "record_scope": scope,
		"record_scope_id": scopeID, "record_edges": edges,
		"record_edges_truncated": edgesTruncated, "record_graph_scope": "recent_records",
		"record_graph_scope_limit": 64, "record_graph_edge_limit": 128,
		"record_graph_edge_byte_limit": 64 * 1024, "recent_limit": 64,
		"workspace": server.identity.WorkspaceID, "profile": server.identity.ProfileID,
		"backend": server.identity.Backend, "health": server.health(),
		"events": events, "sessions": sessions, "project_ids": catalog["project_ids"]}, nil
}

func (panel *Panel) records(ctx context.Context, server *Server, block, cursor, scope, scopeID string) (map[string]any, error) {
	scope, scopeID, err := panelScope(server.identity, scope, scopeID)
	if err != nil {
		return nil, err
	}
	page, err := panel.selected.ListRecords(ctx, engine.ListRecordsRequest{Scope: scope,
		ScopeID: scopeID, LogicalBlockID: block, Limit: 64, Cursor: cursor})
	if err != nil && err.Error() == "list_records scope exceeds 2048 records; select a narrower scope" {
		recent, recentErr := panel.selected.Recent(ctx, engine.RecentRequest{Limit: 64,
			Scope: scope, ScopeID: scopeID, LogicalBlockID: block, IncludeCandidates: true})
		if recentErr != nil {
			return nil, recentErr
		}
		summaries := make([]map[string]any, 0, len(recent))
		for _, record := range recent {
			summaries = append(summaries, panelSummary(record))
		}
		return map[string]any{"ok": true, "records": summaries, "next_cursor": "",
			"scope_limited": true, "scope_limit": 2048, "recent_limit": 64}, nil
	}
	if err != nil {
		return nil, err
	}
	summaries := make([]map[string]any, 0, len(page.Records))
	for _, item := range page.Records {
		record, err := panel.selected.ReadStored(ctx, item["record_id"])
		if err != nil {
			return nil, err
		}
		if record == nil || record.RevisionID != item["revision_id"] {
			return nil, errors.New("list_records snapshot was invalidated")
		}
		summaries = append(summaries, panelSummary(*record))
	}
	return map[string]any{"ok": true, "records": summaries, "next_cursor": page.NextCursor,
		"snapshot_count": page.SnapshotCount, "scope": page.Scope,
		"scope_id": page.ScopeID, "logical_block_id": page.LogicalBlockID}, nil
}

func (panel *Panel) record(ctx context.Context, id string) (map[string]any, error) {
	if id == "" {
		return nil, errors.New("record_id is required")
	}
	record, err := panel.selected.ReadStored(ctx, id)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, errors.New("record was not found")
	}
	history, err := panel.selected.PanelCanonicalHistory(ctx, *record)
	if err != nil {
		return nil, err
	}
	current := make(map[string]bool, len(history))
	for _, revision := range history {
		active, err := panel.selected.PanelIsCurrent(ctx, revision)
		if err != nil {
			return nil, err
		}
		current[revision.BlockID] = active
	}
	start := max(0, len(history)-128)
	versions := make([]map[string]any, 0, len(history)-start)
	for _, revision := range history[start:] {
		summary := panelSummary(revision)
		summary["is_current"] = current[revision.BlockID]
		versions = append(versions, summary)
	}
	return map[string]any{"ok": true, "record": record,
		"is_current": current[record.BlockID], "history": versions,
		"history_truncated": len(history) > 128}, nil
}

func panelAllowedArguments(tool string) map[string]bool {
	switch tool {
	case "recall":
		return map[string]bool{"query": true, "logical_block_ids": true, "include_candidates": true}
	case "remember":
		return map[string]bool{"content": true, "title": true, "requested_block_id": true,
			"operation_id": true}
	case "amend", "correct":
		return map[string]bool{"memory_id": true, "expected_revision": true, "reason": true,
			"content": true, "title": true, "operation_id": true}
	case "amend_block":
		return map[string]bool{"block_id": true, "expected_revision": true, "name": true,
			"synopsis": true, "reason": true}
	}
	return nil
}

func (panel *Panel) call(ctx context.Context, server *Server, session string, body map[string]any) (map[string]any, error) {
	tool, _ := body["tool"].(string)
	arguments, ok := body["arguments"].(map[string]any)
	allowed := panelAllowedArguments(tool)
	if !ok || allowed == nil {
		return nil, errors.New("Unsupported panel operation")
	}
	for key := range arguments {
		if !allowed[key] {
			return nil, errors.New("Unsupported panel arguments")
		}
	}
	arguments = cloneArguments(arguments)
	if tool == "recall" {
		arguments["limit"] = 20
		ids, _ := arguments["logical_block_ids"].([]any)
		arguments["search_all"] = len(ids) == 0
	} else if tool == "amend_block" {
		for key, limit := range map[string]int{"block_id": 500, "name": 500,
			"synopsis": 10_000, "reason": 1_000} {
			value, ok := arguments[key].(string)
			if !ok || strings.TrimSpace(value) == "" || len([]rune(value)) > limit {
				return nil, errors.New("Invalid block edit: " + key)
			}
		}
		revision, ok := arguments["expected_revision"].(json.Number)
		if !ok {
			return nil, errors.New("Invalid block edit: expected_revision")
		}
		number, err := revision.Int64()
		if err != nil || number < 1 {
			return nil, errors.New("Invalid block edit: expected_revision")
		}
	} else {
		for _, key := range []string{"title", "content", "operation_id"} {
			value, ok := arguments[key].(string)
			if !ok || strings.TrimSpace(value) == "" {
				return nil, errors.New(key + " is required")
			}
		}
		if len([]rune(arguments["content"].(string))) > 100_000 || len([]rune(arguments["title"].(string))) > 500 {
			return nil, errors.New("Record exceeds panel editing limit")
		}
		if tool == "amend" || tool == "correct" {
			for _, key := range []string{"memory_id", "expected_revision", "reason"} {
				value, ok := arguments[key].(string)
				if !ok || strings.TrimSpace(value) == "" {
					return nil, errors.New("Amendment requires an exact version and reason")
				}
			}
		}
		if tool == "remember" {
			block, ok := arguments["requested_block_id"].(string)
			if !ok || block == "" {
				return nil, errors.New("Choose a writable topic")
			}
			arguments["kind"] = "observation"
			arguments["source_id"] = "panel:" + session[:min(len(session), 16)]
		}
		if tool == "correct" {
			record, err := panel.selected.ReadStored(ctx, arguments["memory_id"].(string))
			if err != nil {
				return nil, err
			}
			if record == nil || record.BlockMembership == nil {
				return nil, errors.New("record was not found")
			}
			arguments["operation"] = "correct"
			arguments["requested_block_id"] = record.BlockMembership.PrimaryBlockID
			tool = "remember"
		}
	}
	correlation, _ := arguments["operation_id"].(string)
	result := server.panelCall(ctx, "panel:"+session[:min(len(session), 24)], tool, arguments, correlation)
	if result["ok"] == false && ((tool == "amend" && strings.Contains(stringValue(result["error"]),
		"amend expected_revision does not match")) || (tool == "amend_block" &&
		strings.Contains(stringValue(result["error"]), "expected_revision does not match"))) {
		result["reason_code"] = "canonical_revision_conflict"
	}
	if result["ok"] == true && result["canonical_reference"] != nil && result["memory_id"] == nil {
		result["memory_id"] = result["canonical_reference"]
	}
	return result, nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func panelBadInput(err error) bool {
	message := err.Error()
	for _, prefix := range []string{"Panel record scope", "Workspace scope ID", "An exact project",
		"list_records ", "record_id is required", "record was not found",
		"Unsupported panel", "title is required", "content is required",
		"operation_id is required", "Record exceeds panel", "Amendment requires",
		"Choose a writable topic", "Invalid block edit"} {
		if strings.HasPrefix(message, prefix) {
			return true
		}
	}
	return false
}

func (server *Server) panelCall(ctx context.Context, sessionID, tool string,
	arguments map[string]any, correlation string) map[string]any {
	frame := map[string]any{"protocol_version": json.Number("1"),
		"schema_version": json.Number("2"), "api_version": json.Number("2"),
		"request_id": "panel-call", "caller_role": "operator", "session_id": sessionID}
	frame["bootstrap_secret"] = server.secrets["operator"]
	capabilities := make([]any, len(server.capabilities["operator"]))
	for i, capability := range server.capabilities["operator"] {
		capabilities[i] = capability
	}
	frame["capabilities"] = capabilities
	frame["scope_lineage"] = map[string]any{"workspace_id": server.identity.WorkspaceID,
		"project_id": "", "session_id": sessionID, "task_id": "", "subtask_id": "", "user_id": ""}
	issued := server.issueLease(frame)
	if issued["ok"] != true {
		return issued
	}
	bound := cloneArguments(arguments)
	bound["repo_root"], bound["workspace_id"], bound["backend"] = server.identity.RepoRoot,
		server.identity.WorkspaceID, server.identity.Backend
	frame["lease"], frame["tool"], frame["arguments"] = issued["lease"], tool, bound
	if correlation != "" {
		frame["mutation_correlation_id"] = correlation
	}
	return server.dispatchBounded(ctx, func() map[string]any { return server.call(ctx, frame) })
}
