package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/ledger"
	"github.com/frontier-ai-next/Um-mem/memory"
)

type ConsolidateObservationsRequest struct {
	SourceIDs                  []string                    `json:"source_ids"`
	Content                    string                      `json:"content"`
	Title                      string                      `json:"title"`
	Reason                     string                      `json:"reason"`
	VerificationReports        []ledger.VerificationReport `json:"verification_reports"`
	SemanticVerificationReport ledger.VerificationReport   `json:"semantic_verification_report"`
	RequestedBlockID           string                      `json:"requested_block_id"`
	SessionID                  string                      `json:"session_id,omitempty"`
	TaskID                     string                      `json:"task_id,omitempty"`
	SubtaskID                  string                      `json:"subtask_id,omitempty"`
	ProjectID                  string                      `json:"project_id,omitempty"`
}

func normalizedConsolidationIDs(values []string) ([]string, error) {
	seen := map[string]bool{}
	result := []string{}
	for _, raw := range values {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, errors.New("consolidation source IDs must be nonempty")
		}
		if !seen[id] {
			result = append(result, id)
			seen[id] = true
		}
	}
	slices.Sort(result)
	if len(result) < 2 {
		return nil, errors.New("consolidation requires at least two distinct source_ids")
	}
	return result, nil
}

func metadataTextList(metadata map[string]any, key string) []string {
	result := []string{}
	if metadata == nil {
		return result
	}
	switch values := metadata[key].(type) {
	case []any:
		for _, value := range values {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" {
				result = append(result, text)
			}
		}
	case []string:
		for _, value := range values {
			if text := strings.TrimSpace(value); text != "" {
				result = append(result, text)
			}
		}
	}
	return result
}

func consolidatedLineage(records []memory.MemoryRecord, key string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, record := range records {
		for _, id := range metadataTextList(record.Metadata, key) {
			if !seen[id] {
				result = append(result, id)
				seen[id] = true
			}
		}
	}
	slices.Sort(result)
	return result
}

func (engine *Engine) ensureConsolidationEdges(ctx context.Context, recordID string,
	sourceIDs []string, refs []memory.EvidenceRef, transitionID, tribunalID string,
	sessionID, subtaskID string) ([]map[string]any, error) {
	result := make([]map[string]any, 0, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		edge, err := engine.writer.AddEdge(ctx, memory.EdgeRecord{SrcID: recordID, DstID: sourceID,
			EdgeType: "derived_from", WorkspaceID: engine.config.WorkspaceID,
			RunID: sessionID, SubtaskID: subtaskID,
			Reason: "accepted_consolidation_transition", EvidenceRefs: refs,
			Metadata: map[string]any{"transition_id": transitionID, "tribunal_report_id": tribunalID}})
		if err != nil {
			return result, err
		}
		result = append(result, map[string]any{"ok": true, "src_id": edge.SrcID,
			"dst_id": edge.DstID, "edge_type": edge.EdgeType,
			"backend": engine.config.BackendID, "workspace_id": engine.config.WorkspaceID,
			"replayed": edge.Replayed})
	}
	return result, nil
}

func (engine *Engine) consolidationTarget(ctx context.Context, blockID string) (memory.Block, error) {
	if blockID == "" || blockID != strings.TrimSpace(blockID) {
		return memory.Block{}, errors.New("consolidation requires an exact target block")
	}
	if err := engine.RefreshCatalog(); err != nil {
		return memory.Block{}, err
	}
	block, found := engine.current.Load().byID[blockID]
	if !found || block.IndexOnly || len(block.ChildIDs) != 0 || block.Scope != "workspace" {
		return memory.Block{}, errors.New("consolidation target must be a writable workspace leaf")
	}
	state, err := engine.topology.GetBlock(ctx, blockID)
	if err != nil {
		return memory.Block{}, err
	}
	if state != nil && state.Status != "active" {
		return memory.Block{}, errors.New("consolidation target must be active")
	}
	return block, nil
}

// ConsolidateObservations combines two or more current durable host-derived
// records only after verifier ledger checks and the independent tribunal.
func (engine *Engine) ConsolidateObservations(ctx context.Context,
	request ConsolidateObservationsRequest) (map[string]any, error) {
	sourceIDs, err := normalizedConsolidationIDs(request.SourceIDs)
	if err != nil {
		return nil, err
	}
	content, title, reason := strings.TrimSpace(request.Content), strings.TrimSpace(request.Title), strings.TrimSpace(request.Reason)
	if content == "" || len([]rune(content)) > 1024 {
		return nil, errors.New("consolidation content must contain at most 1024 characters")
	}
	if title == "" || len([]rune(title)) > 96 {
		return nil, errors.New("consolidation title must contain at most 96 characters")
	}
	if reason == "" {
		return nil, errors.New("consolidation requires a reason")
	}
	target, err := engine.consolidationTarget(ctx, request.RequestedBlockID)
	if err != nil {
		return nil, err
	}
	keyHash, err := memory.PythonHashValue(map[string]any{"source_ids": sourceIDs, "title": title,
		"content": content, "reason": reason, "requested_block_id": target.BlockID})
	if err != nil {
		return nil, err
	}
	canonicalKey := engine.config.WorkspaceID + ":" + engine.config.ProfileID + ":consolidation:" + keyHash[:24]
	existing, err := engine.ReadStored(ctx, memory.BlockIDForCanonicalKey(canonicalKey))
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.CanonicalKey != canonicalKey || existing.Content != content || existing.Title != title ||
			existing.BlockMembership == nil || existing.BlockMembership.PrimaryBlockID != target.BlockID ||
			!slices.Equal(metadataTextList(existing.Metadata, "source_record_ids"), sourceIDs) {
			return nil, errors.New("consolidation idempotency key conflicts with existing record")
		}
		transitionID, _ := existing.Metadata["transition_id"].(string)
		tribunalID, _ := existing.Metadata["tribunal_report_id"].(string)
		rawIDs := metadataTextList(existing.Metadata, "raw_witness_ids")
		edges, err := engine.ensureConsolidationEdges(ctx, existing.BlockID, sourceIDs,
			existing.EvidenceRefs, transitionID, tribunalID, existing.RunID, existing.SubtaskID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "saved": true, "replayed": true,
			"memory_id": existing.BlockID, "requested_block_id": target.BlockID,
			"actual_block_id": target.BlockID, "route_outcome": "idempotent_replay",
			"status": existing.Status, "authority": existing.Authority,
			"verification_status": existing.VerificationStatus, "source_record_ids": sourceIDs,
			"raw_witness_ids": rawIDs, "provenance_edges": edges}, nil
	}
	if len(request.VerificationReports) == 0 || request.SemanticVerificationReport.ReportID == "" {
		return nil, errors.New("consolidation requires verifier-owned reports")
	}
	resolver, err := ledger.OpenResolver(engine.config.RepoRoot, engine.config.WorkspaceID)
	if err != nil {
		return nil, err
	}
	verificationRefs := []memory.EvidenceRef{}
	for _, report := range append(slices.Clone(request.VerificationReports), request.SemanticVerificationReport) {
		issues, err := resolver.ValidateVerificationReport(report, engine.config.RepoRoot, nil, false)
		if err != nil {
			return nil, err
		}
		if len(issues) != 0 {
			return nil, fmt.Errorf("consolidation verification evidence rejected: %s", strings.Join(issues, ", "))
		}
		verificationRefs = append(verificationRefs, reportEvidenceRef(report))
	}
	verificationRefs, err = mergeEvidenceRefs(nil, verificationRefs)
	if err != nil {
		return nil, err
	}
	sources := make([]memory.MemoryRecord, 0, len(sourceIDs))
	rawWitnessIDs := make([]string, 0, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		source, err := engine.ReadStored(ctx, sourceID)
		if err != nil {
			return nil, err
		}
		if source == nil {
			return nil, fmt.Errorf("consolidation source %q was not found", sourceID)
		}
		if source.WorkspaceID != engine.config.WorkspaceID || source.ProfileID != engine.config.ProfileID {
			return nil, errors.New("consolidation source is outside the client workspace or profile")
		}
		current, err := engine.index.IsCurrentRecord(ctx, *source)
		if err != nil {
			return nil, err
		}
		if !current {
			return nil, errors.New("consolidation source is not the current canonical head")
		}
		if source.BlockMembership == nil || source.Retention != "durable" ||
			source.DeliveryPolicy == "hidden" || source.Scope != "workspace" {
			return nil, errors.New("consolidation accepts only durable workspace semantic sources")
		}
		witnessID, _ := source.Metadata["raw_witness_id"].(string)
		witnessID = strings.TrimSpace(witnessID)
		edges, err := engine.index.EdgesFor(ctx, source.BlockID)
		if err != nil {
			return nil, err
		}
		linked := false
		for _, edge := range edges {
			if edge.SrcID == source.BlockID && edge.DstID == witnessID && edge.EdgeType == "derived_from" {
				linked = true
			}
		}
		if witnessID == "" || !linked {
			return nil, errors.New("consolidation source requires typed provenance to a raw host witness")
		}
		sources = append(sources, *source)
		rawWitnessIDs = append(rawWitnessIDs, witnessID)
	}
	semanticSubject := ledger.ConsolidationSemanticSubject{SourceIDs: sourceIDs,
		Content: content, Title: title, RequestedBlockID: target.BlockID}
	semantic, err := resolver.ValidateConsolidationSemanticReport(request.SemanticVerificationReport,
		semanticSubject, "deterministic-host-proposer-v1", engine.config.RepoRoot, nil)
	if err != nil {
		return nil, err
	}
	signal := memory.ConsolidationSignal{WorkspaceID: engine.config.WorkspaceID,
		SourceEpisodeIDs: sourceIDs, GroupingKey: "typed_host_observations", TaskFamily: "host_lifecycle",
		Trigger: "session_boundary", ProposedAction: "consolidate",
		ExpectedApplicability: "same_workspace_host_observations", Risk: "low", CostBudgetTokens: 1024,
		Rationale: reason, EvidenceRefs: verificationRefs, ExpectedValue: 1,
		EstimatedCostTokens: max(1, memory.EstimateTokens(content)), DependenciesClosed: true,
		RecurrenceCount: 1}
	schedule, err := memory.BuildConsolidationSchedule([]memory.ConsolidationSignal{signal}, "controlled")
	if err != nil {
		return nil, err
	}
	entry := schedule.Entries[0]
	if entry.Disposition != "scheduled" {
		return nil, fmt.Errorf("consolidation was deferred: %s", entry.Reason)
	}
	candidate := map[string]any{"workspace_id": engine.config.WorkspaceID, "authority": "advisory",
		"status": "candidate", "scope": "workspace", "source_record_ids": sourceIDs,
		"claims":             []any{map[string]any{"content": content, "authority": "advisory", "source_record_ids": sourceIDs}},
		"materialized_views": []any{}, "continuation_applicability": signal.ExpectedApplicability,
		"correction_ids":    consolidatedLineage(sources, "correction_of"),
		"contradiction_ids": consolidatedLineage(sources, "contradiction_of")}
	transition, err := memory.ProposeConsolidationTransition(memory.ConsolidationProposal{
		Decision: entry.Decision, SourceRecords: sources, NewEvidenceRefs: verificationRefs,
		ProposedOperations: []string{"consolidate_host_observations"}, PostStateCandidate: candidate,
		SourceCoverageAfter: sourceIDs, PolicyVersion: "host-lifecycle-v1",
		ModelVersion: "deterministic-host-proposer-v1", PromptVersion: "none"})
	if err != nil {
		return nil, err
	}
	tribunal, err := memory.AssessConsolidationTribunal(transition, entry.Decision,
		sources, semantic, "assessment-"+transition.TransitionID)
	if err != nil {
		return nil, err
	}
	shadow := memory.NewConsolidationShadowStore()
	snapshot, err := shadow.Stage(engine.config.WorkspaceID, transition)
	if err != nil {
		return nil, err
	}
	resolution, err := shadow.Resolve(snapshot.SnapshotID, tribunal, true)
	if err != nil {
		return nil, err
	}
	if resolution.Disposition != "committed" || resolution.CommitReceipt == nil {
		return map[string]any{"ok": false, "saved": false, "route_outcome": "tribunal_rejected",
			"schedule": schedule, "transition": transition, "tribunal": tribunal,
			"shadow_resolution": resolution}, nil
	}
	record, err := memory.NewMemoryRecord(content, engine.config.WorkspaceID)
	if err != nil {
		return nil, err
	}
	record.ProfileID, record.Title, record.CanonicalKey = engine.config.ProfileID, title, canonicalKey
	record.BlockID = memory.BlockIDForCanonicalKey(canonicalKey)
	record.Kind, record.Status, record.Scope, record.Authority = "observation", "candidate", "workspace", "advisory"
	record.Retention, record.DeliveryPolicy, record.Temperature = target.Retention, target.DeliveryPolicy, target.Temperature
	record.TrustLevel, record.VerificationStatus = "source_observed", "observed"
	record.Producer, record.SourceBackend = "supervisor_tribunal", "um_mem"
	record.SessionID, record.TaskID, record.SubtaskID, record.ProjectID = request.SessionID, request.TaskID, request.SubtaskID, request.ProjectID
	record.RunID = request.SessionID
	record.Tags = append(slices.Clone(target.Tags), "consolidated", "host_observed")
	record.EvidenceRefs = verificationRefs
	record.Metadata = map[string]any{"record_kind": "consolidated_host_observation",
		"source_record_ids": sourceIDs, "raw_witness_ids": rawWitnessIDs,
		"transition_id": transition.TransitionID, "tribunal_report_id": tribunal.ReportID,
		"shadow_commit_receipt_id": resolution.CommitReceipt.ReceiptID}
	semanticRow := resolver.Row(request.SemanticVerificationReport.ReportID)
	proofTime, ok := semanticRow["ts"].(float64)
	if !ok || proofTime <= 0 {
		return nil, errors.New("consolidation semantic proof has no event time")
	}
	stableTime := time.Unix(0, int64(proofTime*1e9)).UTC().Format("2006-01-02T15:04:05.000000Z")
	record.RecordedAt, record.CreatedAt, record.UpdatedAt = stableTime, stableTime, stableTime
	record.DuplicateClusterID, err = memory.DuplicateClusterID(record)
	if err != nil {
		return nil, err
	}
	record.RevisionID, err = memory.RevisionIDForRecord(canonicalKey, record, "insert", "")
	if err != nil {
		return nil, err
	}
	if err := engine.BindRootRecord(ctx, &record, target.BlockID); err != nil {
		return nil, err
	}
	commitReceipt := memory.ConsolidationCommitReceipt{ReceiptID: resolution.CommitReceipt.ReceiptID,
		EvidenceRefs: resolution.CommitReceipt.EvidenceRefs, CommittedIDs: []string{record.BlockID}}
	write, err := engine.PutConsolidationRoot(ctx, record, commitReceipt)
	if err != nil {
		return nil, err
	}
	edges, err := engine.ensureConsolidationEdges(ctx, record.BlockID, sourceIDs,
		verificationRefs, transition.TransitionID, tribunal.ReportID,
		request.SessionID, request.SubtaskID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "saved": true, "replayed": false,
		"memory_id": record.BlockID, "requested_block_id": target.BlockID,
		"actual_block_id": target.BlockID, "route_outcome": "consolidated",
		"status": record.Status, "authority": record.Authority,
		"verification_status": record.VerificationStatus, "source_record_ids": sourceIDs,
		"raw_witness_ids": rawWitnessIDs, "schedule": schedule, "transition": transition,
		"tribunal": tribunal, "shadow_resolution": resolution,
		"consolidation_receipt": commitReceipt, "provenance_edges": edges,
		"commit_revision": write.CommitRevision}, nil
}
