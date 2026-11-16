package mcpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func contextSnapshot(value any) (string, error) {
	encoded, err := jsoncompat.MarshalPython(value, false)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func appendUniqueID(values []string, raw string) []string {
	id := strings.TrimSpace(raw)
	if id == "" {
		return values
	}
	for _, existing := range values {
		if existing == id {
			return values
		}
	}
	return append(values, id)
}

func candidatePoolIDs(metadata map[string]any) []string {
	result := []string{}
	switch values := metadata["candidate_pool_ids"].(type) {
	case []string:
		for _, id := range values {
			result = appendUniqueID(result, id)
		}
	case []any:
		for _, raw := range values {
			if id, ok := raw.(string); ok {
				result = appendUniqueID(result, id)
			}
		}
	}
	return result
}

// recordContextDecision mirrors the Python context builder's neutral
// candidate/selected/rendered funnel. It never infers application or outcome.
func recordContextDecision(ctx context.Context, selected *engine.Engine, plan memory.RecallPlan,
	records []memory.MemoryRecord, packed memory.PackedEvidencePayload, latency time.Duration) (string, error) {
	if len(records) == 0 {
		return "", nil
	}
	candidates, selectedIDs, renderedIDs := []string{}, []string{}, []string{}
	for _, record := range records {
		for _, id := range candidatePoolIDs(record.Metadata) {
			candidates = appendUniqueID(candidates, id)
		}
		selectedIDs = appendUniqueID(selectedIDs, record.BlockID)
	}
	for _, id := range selectedIDs {
		candidates = appendUniqueID(candidates, id)
	}
	for _, item := range packed.Items {
		metadata, _ := item["metadata"].(map[string]any)
		id, _ := metadata["block_id"].(string)
		if id == "" {
			id, _ = item["source"].(string)
		}
		renderedIDs = appendUniqueID(renderedIDs, id)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	selectionID, _ := records[0].Metadata["decision_id"].(string)
	material, err := jsoncompat.MarshalPython([]any{hex.EncodeToString(nonce[:]), selectionID, plan.NormalizedQuery,
		plan.WorkspaceID, plan.RunID, candidates, selectedIDs, packed.RenderedContext}, false)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(material)
	decisionID := "decision_" + hex.EncodeToString(digest[:])
	canonicalSnapshot, err := contextSnapshot(candidates)
	if err != nil {
		return "", err
	}
	contextHash, err := contextSnapshot(packed.RenderedContext)
	if err != nil {
		return "", err
	}
	runID := plan.RunID
	if runID == "" {
		runID = "unbound"
	}
	liveHash, err := contextSnapshot([]any{plan.WorkspaceID, runID, append([]string{}, plan.ScopeChain...)})
	if err != nil {
		return "", err
	}
	queryDigest := sha256.Sum256([]byte(plan.NormalizedQuery))
	taskSignature := "recall:" + hex.EncodeToString(queryDigest[:])[:24]
	actionTraceID := "context:" + decisionID
	evidence := []memory.EvidenceRef{{RefType: "ledger_event", RefID: "context-decision:" + decisionID,
		ProducedBy: "harness", ProducerID: "um_mem:context_builder"}}
	event := memory.MemoryDecisionEvent{DecisionID: decisionID, WorkspaceID: plan.WorkspaceID, RunID: runID,
		TaskSignature: taskSignature, ActionTraceID: actionTraceID, Action: "recall",
		CanonicalSnapshotSHA256: canonicalSnapshot, ContextSnapshotSHA256: contextHash,
		LiveStateSnapshotSHA256: liveHash, LiveTrigger: "context_build", CandidateIDs: candidates,
		SelectedCandidateIDs: selectedIDs, PolicyRevision: "evidence-requirements-specificity-v2",
		CatalogRevision: selected.CatalogRevisionID(), CostTokens: packed.EstimatedTokens,
		CostLatencyMS: int(latency.Milliseconds()), DownstreamOutcome: "neutral_pending_verifier",
		EvidenceRefs: evidence}
	if _, err := selected.RecordMemoryDecision(ctx, event); err != nil {
		return "", err
	}
	sequence := 0
	stages := []struct {
		kind string
		ids  []string
	}{{"candidate", candidates}, {"selected", selectedIDs}, {"rendered", renderedIDs}}
	for _, stage := range stages {
		for _, id := range stage.ids {
			material := fmt.Sprintf("%s|%s|%s|%d", decisionID, stage.kind, id, sequence)
			eventDigest := sha256.Sum256([]byte(material))
			usage := memory.MemoryUsageEvent{EventID: "usage_" + hex.EncodeToString(eventDigest[:]),
				DecisionID: decisionID, MemoryID: id, WorkspaceID: plan.WorkspaceID,
				TaskSignature: taskSignature, ActionTraceID: actionTraceID, UsageType: stage.kind,
				Sequence: sequence, EvidenceRefs: evidence}
			if _, err := selected.RecordMemoryUsageEvent(ctx, usage); err != nil {
				return "", err
			}
			sequence++
		}
	}
	if sequence == 0 {
		return "", errors.New("context decision has no candidate usage")
	}
	return decisionID, nil
}
