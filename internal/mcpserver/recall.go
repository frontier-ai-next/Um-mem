package mcpserver

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/frontier-ai-next/Um-mem/internal/telemetry"
	"github.com/frontier-ai-next/Um-mem/memory"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/text/cases"
)

type RecallArgs struct {
	Query             string   `json:"query"`
	Limit             *int     `json:"limit,omitempty"`
	Tags              []string `json:"tags,omitempty"`
	SourceHorizon     string   `json:"source_horizon,omitempty"`
	IncludeCandidates *bool    `json:"include_candidates,omitempty"`
	LogicalBlockIDs   []string `json:"logical_block_ids,omitempty"`
	SearchAll         bool     `json:"search_all,omitempty"`
	RetrievalMode     string   `json:"retrieval_mode,omitempty"`
	RecordIDs         []string `json:"record_ids,omitempty"`
	MaxTokens         *int     `json:"max_tokens,omitempty"`
	SourceSpeakers    []string `json:"source_speakers,omitempty"`
	TemporalMode      string   `json:"temporal_mode,omitempty"`
	AsOf              string   `json:"as_of,omitempty"`
	KnownAsOf         string   `json:"known_as_of,omitempty"`
	RevisionID        string   `json:"revision_id,omitempty"`
	IncludeManager    bool     `json:"include_manager,omitempty"`
}

func addRecallTool(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string, lineage map[string]string) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "recall",
		Description: "Recall bounded workspace evidence from the selected native backend.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args RecallArgs) (*mcp.CallToolResult, map[string]any, error) {
		payload, err := RecallPayload(ctx, selected, args, workspaceID, backendID,
			boundHostScopeChain(workspaceID, lineage), lineage["run_id"])
		if err != nil {
			return nil, responseError(err, workspaceID, backendID), nil
		}
		return nil, payload, nil
	})
}

// RecallPayload performs the shared MCP and local-service recall projection.
// Callers must bind scopeChain and runID to their authenticated context.
func RecallPayload(ctx context.Context, selected *engine.Engine, args RecallArgs,
	workspaceID, backendID string, scopeChain []string, runID string) (map[string]any, error) {
	return recallPayload(ctx, selected, args, workspaceID, backendID, scopeChain, runID, "", false, false, false, nil, nil)
}

// RecallPayloadForUser permits the local service to include the manager layer
// only with a user ID taken from the authenticated lease.
func RecallPayloadForUser(ctx context.Context, selected *engine.Engine, args RecallArgs,
	workspaceID, backendID string, scopeChain []string, runID, userID string) (map[string]any, error) {
	return recallPayload(ctx, selected, args, workspaceID, backendID, scopeChain, runID, userID, false, false, false, nil, nil)
}

// BuildContextPayload applies the additional overlay delivery gate and may
// render the packed context for the authenticated caller.
func BuildContextPayload(ctx context.Context, selected *engine.Engine, args RecallArgs,
	workspaceID, backendID string, scopeChain []string, runID string, render bool) (map[string]any, error) {
	ctx, span := telemetry.StartProfile(ctx, selected.RepoRoot(), telemetry.ProfileOptions{
		Operation: "build_context", Component: "memory_client", WorkspaceID: workspaceID,
		RunID: runID, Backend: backendID,
		Input: map[string]any{"query": args.Query, "limit": args.Limit,
			"logical_block_ids": args.LogicalBlockIDs, "render": render}})
	payload, err := recallPayload(ctx, selected, args, workspaceID, backendID,
		scopeChain, runID, "", true, render, false, nil, nil)
	if err == nil {
		span.SetOutput(payload)
	}
	span.End(err)
	return payload, err
}

func BuildContextPayloadForUser(ctx context.Context, selected *engine.Engine, args RecallArgs,
	workspaceID, backendID string, scopeChain []string, runID, userID string,
	render bool) (map[string]any, error) {
	ctx, span := telemetry.StartProfile(ctx, selected.RepoRoot(), telemetry.ProfileOptions{
		Operation: "build_context", Component: "memory_client", WorkspaceID: workspaceID,
		UserID: userID, RunID: runID, Backend: backendID,
		Input: map[string]any{"query": args.Query, "limit": args.Limit,
			"logical_block_ids": args.LogicalBlockIDs, "render": render}})
	payload, err := recallPayload(ctx, selected, args, workspaceID, backendID,
		scopeChain, runID, userID, true, render, false, nil, nil)
	if err == nil {
		span.SetOutput(payload)
	}
	span.End(err)
	return payload, err
}

// BuildActivationLeasePayload applies the context delivery gate and returns a
// bounded, non-directive lease with its selection trace kept out of prompt text.
func BuildActivationLeasePayload(ctx context.Context, selected *engine.Engine, args RecallArgs,
	workspaceID, backendID string, scopeChain []string, runID string) (memory.ActivationLease, error) {
	payload, err := recallPayload(ctx, selected, args, workspaceID, backendID,
		scopeChain, runID, "", true, false, true, nil, nil)
	if err != nil {
		return memory.ActivationLease{}, err
	}
	lease, ok := payload["activation_lease"].(memory.ActivationLease)
	if !ok {
		return memory.ActivationLease{}, errors.New("activation lease was not produced")
	}
	return lease, nil
}

type ExplainRecallRequest struct {
	RecallArgs
	RunID        string   `json:"run_id"`
	GoldBlockIDs []string `json:"gold_block_ids"`
	GoldLiterals []string `json:"gold_literals"`
}

// ExplainRecallPayload runs the same bounded overlay selection and returns an
// out-of-prompt explanation with explicit candidate and literal quality gates.
func ExplainRecallPayload(ctx context.Context, selected *engine.Engine, request ExplainRecallRequest,
	workspaceID, backendID string, scopeChain []string, runID string) (memory.RecallExplanation, error) {
	payload, err := recallPayload(ctx, selected, request.RecallArgs, workspaceID, backendID,
		scopeChain, runID, "", true, false, false, &request, nil)
	if err != nil {
		return memory.RecallExplanation{}, err
	}
	result, ok := payload["explanation"].(memory.RecallExplanation)
	if !ok {
		return memory.RecallExplanation{}, errors.New("recall explanation was not produced")
	}
	return result, nil
}

type DeepReadRequest struct {
	RecallArgs
	RunID                         string           `json:"run_id"`
	LogicalBlockID                string           `json:"logical_block_id"`
	BlockDescriptors              []map[string]any `json:"block_descriptors,omitempty"`
	LastKnownGoodBlockDescriptors []map[string]any `json:"last_known_good_block_descriptors,omitempty"`
}

// DeepReadPayload keeps the whole selection inside one logical block and one
// workspace. The returned traces remain outside the rendered prompt context.
func DeepReadPayload(ctx context.Context, selected *engine.Engine, request DeepReadRequest,
	workspaceID, backendID string) (memory.DeepReadResult, error) {
	blockID := strings.TrimSpace(request.LogicalBlockID)
	if blockID == "" {
		return memory.DeepReadResult{}, errors.New("deep_read requires one logical_block_id")
	}
	if request.IncludeManager || request.SearchAll || len(request.LogicalBlockIDs) > 0 &&
		(len(request.LogicalBlockIDs) != 1 || request.LogicalBlockIDs[0] != blockID) {
		return memory.DeepReadResult{}, errors.New("deep_read cannot widen its logical block or scope")
	}
	limit := 16
	if request.Limit != nil {
		limit = *request.Limit
	}
	limit = min(32, max(1, limit))
	maxTokens := 2048
	if request.MaxTokens != nil {
		maxTokens = *request.MaxTokens
	}
	maxTokens = min(4096, max(0, maxTokens))
	request.Limit, request.MaxTokens = &limit, &maxTokens
	request.LogicalBlockIDs = []string{blockID}
	request.SearchAll = false
	request.IncludeManager = false
	request.TemporalMode, request.AsOf, request.KnownAsOf, request.RevisionID = "current", "", "", ""
	if request.BlockDescriptors == nil {
		descriptor, err := selected.CurrentReaderGuidance(blockID)
		if err != nil {
			return memory.DeepReadResult{}, err
		}
		request.BlockDescriptors = []map[string]any{descriptor}
	}
	payload, err := recallPayload(ctx, selected, request.RecallArgs, workspaceID, backendID,
		[]string{"workspace:" + workspaceID}, request.RunID, "", true, false, false, nil, &request)
	if err != nil {
		return memory.DeepReadResult{}, err
	}
	result, ok := payload["deep_read"].(memory.DeepReadResult)
	if !ok {
		return memory.DeepReadResult{}, errors.New("deep read result was not produced")
	}
	return result, nil
}

func recallPayload(ctx context.Context, selected *engine.Engine, args RecallArgs,
	workspaceID, backendID string, scopeChain []string, runID string,
	managerUserID string, buildContext, renderContext, activationLease bool,
	explain *ExplainRecallRequest, deep *DeepReadRequest) (map[string]any, error) {
	if args.IncludeManager && (managerUserID == "" ||
		managerUserID != strings.TrimSpace(managerUserID)) {
		return nil, errors.New("manager recall requires an authenticated user identity")
	}
	if args.SourceSpeakers != nil {
		if len(args.SourceSpeakers) < 1 || len(args.SourceSpeakers) > 8 {
			return nil, errors.New("source_speakers must be 1-8 nonempty names")
		}
		for _, speaker := range args.SourceSpeakers {
			if strings.TrimSpace(speaker) == "" {
				return nil, errors.New("source_speakers must be 1-8 nonempty names")
			}
		}
	}
	_, profileID := selected.Scope()
	plan := memory.NewRecallPlan(args.Query)
	plan.WorkspaceID, plan.ProfileID = workspaceID, profileID
	plan.ScopeChain, plan.RunID = scopeChain, runID
	plan.LogicalBlockIDs, plan.RecordIDs = args.LogicalBlockIDs, args.RecordIDs
	plan.SearchAll = args.SearchAll || len(args.LogicalBlockIDs) == 0 && len(args.RecordIDs) == 0
	plan.Tags = args.Tags
	plan.AsOf, plan.KnownAsOf, plan.RevisionID = args.AsOf, args.KnownAsOf, args.RevisionID
	plan.Reason = "explicit_block_first_recall"
	if plan.SearchAll {
		plan.Reason = "explicit_search_all"
	}
	if deep != nil {
		current := make([]any, 0, len(deep.BlockDescriptors))
		for _, descriptor := range deep.BlockDescriptors {
			current = append(current, descriptor)
		}
		lkg := make([]any, 0, len(deep.LastKnownGoodBlockDescriptors))
		for _, descriptor := range deep.LastKnownGoodBlockDescriptors {
			lkg = append(lkg, descriptor)
		}
		plan.Metadata = map[string]any{"block_descriptors": current,
			"last_known_good_block_descriptors": lkg}
	}
	if args.RetrievalMode != "" {
		plan.RetrievalMode = args.RetrievalMode
	}
	if args.TemporalMode != "" {
		plan.TemporalMode = args.TemporalMode
	}
	if args.SourceHorizon != "" {
		plan.SourceHorizon = args.SourceHorizon
	}
	if args.IncludeCandidates == nil {
		plan.IncludeCandidates = true
	} else {
		plan.IncludeCandidates = *args.IncludeCandidates
	}
	plan.Limit = 4
	if args.Limit != nil {
		plan.Limit = *args.Limit
	}
	if args.MaxTokens != nil {
		plan.MaxTokens = *args.MaxTokens
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	requestedLimit := plan.Limit
	if len(args.SourceSpeakers) > 0 {
		plan.Limit = max(plan.Limit, 64)
	}
	started := time.Now()
	records, err := selected.Recall(ctx, plan)
	if err != nil {
		return nil, err
	}
	retrievalMS := float64(time.Since(started).Microseconds()) / 1000
	if len(args.SourceSpeakers) > 0 {
		records = filterSourceSpeakers(records, args.SourceSpeakers, requestedLimit)
	}
	if args.IncludeManager && len(records) < requestedLimit {
		managerPlan := plan
		managerPlan.Limit = requestedLimit - len(records)
		if len(args.SourceSpeakers) > 0 {
			managerPlan.Limit = max(managerPlan.Limit, 64)
		}
		managerRecords, err := selected.RecallManager(ctx, managerPlan, managerUserID)
		if err != nil {
			return nil, err
		}
		if len(args.SourceSpeakers) > 0 {
			managerRecords = filterSourceSpeakers(managerRecords, args.SourceSpeakers,
				requestedLimit-len(records))
		}
		seen := make(map[string]bool, len(records))
		for _, record := range records {
			seen[record.WorkspaceID+"\x00"+record.BlockID] = true
		}
		for _, record := range managerRecords {
			if len(plan.RecordIDs) > 0 && !slices.Contains(plan.RecordIDs, record.BlockID) {
				continue
			}
			if plan.RevisionID != "" && record.RevisionID != plan.RevisionID {
				continue
			}
			key := record.WorkspaceID + "\x00" + record.BlockID
			if seen[key] {
				continue
			}
			records = append(records, record)
			seen[key] = true
			if len(records) >= requestedLimit {
				break
			}
		}
	}
	if len(records) > requestedLimit {
		records = records[:requestedLimit]
	}
	if deep != nil {
		bounded := make([]memory.MemoryRecord, 0, len(records))
		for _, record := range records {
			if record.WorkspaceID != workspaceID || record.BlockMembership == nil ||
				(record.SourceBackend != "" && record.SourceBackend != backendID) {
				continue
			}
			inside := record.BlockMembership.PrimaryBlockID == deep.LogicalBlockID
			for _, secondary := range record.BlockMembership.SecondaryBlockIDs {
				inside = inside || secondary == deep.LogicalBlockID
			}
			if inside {
				bounded = append(bounded, record)
			}
		}
		records = bounded
	}
	if buildContext {
		admitted := make([]memory.MemoryRecord, 0, len(records))
		for _, record := range records {
			if record.DeliveryPolicy == "always_on" || record.DeliveryPolicy == "working_set" ||
				record.DeliveryPolicy == "on_demand" {
				admitted = append(admitted, record)
			}
		}
		records = admitted
	}
	raw := make([]map[string]any, 0, len(records))
	for _, record := range records {
		raw = append(raw, rawRecallItem(record, buildContext))
	}
	packed, err := memory.PackEvidencePayload(raw, memory.PackOptions{MaxTokens: plan.MaxTokens})
	if err != nil {
		return nil, err
	}
	contextDecisionID := ""
	if buildContext {
		contextDecisionID, err = recordContextDecision(ctx, selected, plan, records, packed, time.Since(started))
		if err != nil {
			return nil, err
		}
		if contextDecisionID != "" {
			for _, item := range packed.Items {
				metadata, _ := item["metadata"].(map[string]any)
				if metadata == nil {
					metadata = map[string]any{}
					item["metadata"] = metadata
				}
				metadata["context_decision_id"] = contextDecisionID
			}
		}
	}
	hits := packed.Items
	for _, item := range hits {
		metadata, _ := item["metadata"].(map[string]any)
		id, _ := metadata["block_id"].(string)
		for _, record := range records {
			if record.BlockID != id {
				continue
			}
			if score, ok := record.Metadata["retrieval_score"]; ok {
				metadata["retrieval_score"] = score
			} else if score, ok := record.Metadata["native_score"]; ok {
				metadata["retrieval_score"] = score
			}
			if _, exists := item["score_mode"]; !exists && metadata["retrieval_score"] != nil {
				mode, _ := record.Metadata["native_score_mode"].(string)
				if mode == "" {
					mode = "native_undeclared"
				}
				item["score_mode"] = mode
			}
			break
		}
	}
	diagnostics := map[string]any{
		"retrieval_ms": retrievalMS, "recall_plan_reason": plan.Reason,
		"normalized_query": plan.NormalizedQuery, "query_type": plan.QueryType,
		"retrieval_mode": plan.RetrievalMode, "logical_block_ids": plan.LogicalBlockIDs,
		"record_ids": plan.RecordIDs, "source_horizon": plan.SourceHorizon,
		"scope_chain": plan.ScopeChain, "include_candidates": plan.IncludeCandidates,
		"run_id":                           plan.RunID,
		"requested_context_token_budget":   plan.MaxTokens,
		"allocated_context_token_budget":   plan.MaxTokens,
		"truncated_item_count":             packed.TruncatedItemCount,
		"context_char_budget":              packed.TokenBudget * 4,
		"context_token_budget":             packed.TokenBudget,
		"context_payload_chars":            len([]rune(packed.RenderedContext)),
		"context_payload_estimated_tokens": packed.EstimatedTokens,
		"metadata_overhead_tokens":         packed.MetadataTokens,
		"exact_literal_capsule":            packed.ExactLiteralCapsule,
		"exact_literal_losses":             packed.ExactLiteralLosses,
		"visibility":                       packed.Visibility,
	}
	if len(args.SourceSpeakers) > 0 {
		diagnostics["source_speakers"] = args.SourceSpeakers
		diagnostics["native_candidate_limit"] = plan.Limit
		diagnostics["source_speaker_filter_complete"] = false
		diagnostics["source_speaker_filter_stage"] = "after_native_candidate_limit"
	}
	if explain != nil {
		result := memory.BuildRecallExplanation(plan, records, packed, backendID,
			explain.GoldBlockIDs, explain.GoldLiterals)
		return map[string]any{"ok": true, "explanation": result}, nil
	}
	if deep != nil {
		lease, err := memory.ActivationLeaseFromPayload(plan, packed, backendID, "deep_block_local")
		if err != nil {
			return nil, err
		}
		result := memory.DeepReadResultFromLease(lease, plan, backendID, deep.LogicalBlockID, plan.Limit)
		return map[string]any{"ok": true, "deep_read": result}, nil
	}
	if activationLease {
		lease, err := memory.ActivationLeaseFromPayload(plan, packed, backendID, "fast_one_hop")
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "activation_lease": lease}, nil
	}
	if buildContext {
		diagnostics["rendered_item_count"] = len(hits)
		diagnostics["final_context_chars"] = len([]rune(packed.RenderedContext))
		if err := recordPackedRender(ctx, selected, records, packed); err != nil {
			return nil, err
		}
		result := map[string]any{"ok": true, "workspace_id": workspaceID,
			"backend": backendID, "run_id": runID, "include_manager": args.IncludeManager,
			"delivery_policy": "on_demand", "authority": "advisory",
			"diagnostics": diagnostics, "items": hits}
		if renderContext {
			result["rendered_context"] = packed.RenderedContext
		}
		return result, nil
	}
	return map[string]any{"ok": true, "workspace_id": workspaceID, "backend": backendID,
		"source_horizon": plan.SourceHorizon, "scope_chain": plan.ScopeChain,
		"include_candidates": plan.IncludeCandidates, "include_manager": args.IncludeManager,
		"run_id": plan.RunID, "diagnostics": diagnostics, "hits": hits}, nil
}

func filterSourceSpeakers(records []memory.MemoryRecord, requested []string, limit int) []memory.MemoryRecord {
	fold := cases.Fold()
	selected := make(map[string]bool, len(requested))
	for _, speaker := range requested {
		selected[fold.String(strings.TrimSpace(speaker))] = true
	}
	filtered := make([]memory.MemoryRecord, 0, min(limit, len(records)))
	for _, record := range records {
		segments, _ := record.Metadata["source_segments"].([]any)
		matched := false
		for _, segment := range segments {
			fields, _ := segment.(map[string]any)
			name, _ := fields["speaker"].(string)
			if selected[fold.String(strings.TrimSpace(name))] && name != "" {
				matched = true
				break
			}
		}
		if matched {
			filtered = append(filtered, record)
			if len(filtered) == limit {
				break
			}
		}
	}
	return filtered
}

func rawRecallItem(record memory.MemoryRecord, forceNonDirective bool) map[string]any {
	metadata := make(map[string]any, len(record.Metadata)+18)
	for key, value := range record.Metadata {
		metadata[key] = value
	}
	metadata["block_id"] = record.BlockID
	metadata["workspace_id"] = record.WorkspaceID
	metadata["status"] = record.Status
	metadata["retention"] = record.Retention
	metadata["delivery_policy"] = record.DeliveryPolicy
	metadata["temperature"] = record.Temperature
	metadata["authority"] = record.Authority
	authority := record.Authority
	if forceNonDirective {
		authority = "advisory"
		metadata["source_authority"] = record.Authority
		metadata["authority"] = authority
	}
	metadata["trust_level"] = record.TrustLevel
	metadata["scope"] = record.Scope
	metadata["run_id"] = record.RunID
	metadata["source_backend"] = record.SourceBackend
	metadata["sensitivity"] = record.Sensitivity
	metadata["content_mode"] = record.ContentMode
	metadata["operational_risk"] = record.OperationalRisk
	metadata["verification_status"] = record.VerificationStatus
	metadata["promotion_decision"] = record.PromotionDecision
	metadata["evidence_ref_count"] = len(record.EvidenceRefs)
	sourcePath, _ := metadata["source_path"].(string)
	if sourcePath == "" {
		sourcePath, _ = metadata["path"].(string)
	}
	whyMatched, _ := metadata["why_matched"].(string)
	if whyMatched == "" {
		whyMatched, _ = metadata["recall_plan_reason"].(string)
	}
	if whyMatched == "" {
		whyMatched = "memory_recall"
	}
	return map[string]any{
		"text": record.Content, "title": record.Title, "source": record.BlockID,
		"source_path": sourcePath, "score": metadata["retrieval_score"],
		"score_mode": metadata["native_score_mode"], "kind": record.Kind,
		"tags": record.Tags, "metadata": metadata,
		"status": "supplemental non-directive", "authority": authority,
		"why_matched": whyMatched,
	}
}
