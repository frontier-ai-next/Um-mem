package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/ledger"
	"github.com/frontier-ai-next/Um-mem/memory"
)

// PromotionReviewRequest describes the proof boundary for one exact candidate
// revision. Publication must still reserve that revision with a canonical-head
// compare-and-swap after this review succeeds.
type PromotionReviewRequest struct {
	VerificationSubjectRequest
	Reports                    []ledger.VerificationReport `json:"evidence_refs"`
	WorkspaceRoot              string                      `json:"workspace_root,omitempty"`
	ChangedFiles               []string                    `json:"changed_files,omitempty"`
	AllowWorkspaceHashMismatch bool                        `json:"allow_workspace_hash_mismatch_if_diff_matches,omitempty"`
}

type PromotionReview struct {
	Source               memory.MemoryRecord
	Subject              memory.CandidateVerificationSubject
	SemanticReceipt      map[string]any
	ProcedureGateReceipt map[string]any
	PromotionRefs        []memory.EvidenceRef
	Decision             memory.TribunalDecision
}

func reportEvidenceRef(report ledger.VerificationReport) memory.EvidenceRef {
	hash := report.LedgerHash
	ref := memory.EvidenceRef{RefType: "verification_report", RefID: report.ReportID,
		Hash: &hash, ProducedBy: "verifier", ProducerID: report.VerifierID}
	if report.ProducedAfterEventID != "" {
		ref.CreatedAfterEvent = &report.ProducedAfterEventID
	}
	return ref
}

func procedureEpisodeIDs(metadata map[string]any) ([]string, string, error) {
	applicability, ok := metadata["procedure_applicability"].(string)
	if !ok || strings.TrimSpace(applicability) == "" {
		return nil, "", errors.New("procedure requires applicability")
	}
	signature, ok := metadata["source_task_signature"].(string)
	if !ok || strings.TrimSpace(signature) == "" {
		return nil, "", errors.New("procedure requires a task signature")
	}
	var raw []any
	switch values := metadata["source_episode_ids"].(type) {
	case []any:
		raw = values
	case []string:
		for _, value := range values {
			raw = append(raw, value)
		}
	}
	if len(raw) == 0 {
		return nil, "", errors.New("procedure requires source episodes")
	}
	ids := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, item := range raw {
		id, ok := item.(string)
		if !ok || strings.TrimSpace(id) == "" || seen[id] {
			return nil, "", errors.New("procedure source episodes must have unique nonempty identities")
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, signature, nil
}

func promotionFlagEnabled(value any) bool {
	switch selected := value.(type) {
	case bool:
		return selected
	case string:
		return strings.Contains("|1|true|yes|on|", "|"+strings.ToLower(strings.TrimSpace(selected))+"|")
	case int:
		return selected == 1
	case int64:
		return selected == 1
	case float64:
		return selected == 1
	case json.Number:
		return selected.String() == "1"
	default:
		return false
	}
}

// ValidatePromotion combines the ledger's freshness and independent semantic
// proof with source, procedure, and graph checks before a publication write.
func (engine *Engine) ValidatePromotion(ctx context.Context,
	request PromotionReviewRequest) (PromotionReview, error) {
	if len(request.Reports) == 0 {
		return PromotionReview{}, errors.New("promotion requires a dedicated semantic verification report")
	}
	source, err := engine.ReadStored(ctx, request.MemoryID)
	if err != nil {
		return PromotionReview{}, err
	}
	if source == nil || source.RevisionID != request.ExpectedSourceRevision ||
		source.Status != "candidate" || source.Kind == "skill_index" ||
		promotionFlagEnabled(source.Metadata["index_only"]) {
		return PromotionReview{}, errors.New("promotion requires an exact current publishable candidate")
	}
	current, err := engine.index.IsCurrentRecord(ctx, *source)
	if err != nil {
		return PromotionReview{}, err
	}
	if !current {
		return PromotionReview{}, errors.New("promotion source is no longer the canonical head")
	}
	subject, err := engine.VerificationSubject(ctx, request.VerificationSubjectRequest)
	if err != nil {
		return PromotionReview{}, err
	}
	resolver, err := ledger.OpenResolver(engine.config.RepoRoot, engine.config.WorkspaceID)
	if err != nil {
		return PromotionReview{}, err
	}
	workspaceRoot := request.WorkspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = engine.config.RepoRoot
	}
	for _, ref := range request.SourceEvidenceRefs {
		if issues := resolver.ValidateRef(ref); len(issues) != 0 {
			return PromotionReview{}, fmt.Errorf("promotion source evidence rejected: %s", strings.Join(issues, ", "))
		}
	}
	refs := append([]memory.EvidenceRef(nil), request.SourceEvidenceRefs...)
	var semanticReceipt map[string]any
	for _, report := range request.Reports {
		issues, err := resolver.ValidateVerificationReport(report, workspaceRoot,
			request.ChangedFiles, request.AllowWorkspaceHashMismatch)
		if err != nil {
			return PromotionReview{}, err
		}
		if len(issues) != 0 {
			return PromotionReview{}, fmt.Errorf("promotion verification evidence rejected: %s", strings.Join(issues, ", "))
		}
		row := resolver.Row(report.ReportID)
		if row != nil && row["tool"] == "candidate_semantic_verification" {
			result, err := resolver.ValidateCandidateSemanticReport(report, subject,
				workspaceRoot, request.ChangedFiles, request.AllowWorkspaceHashMismatch)
			if err != nil {
				return PromotionReview{}, err
			}
			semanticReceipt = map[string]any{"result": result, "report": report}
		}
		refs = append(refs, reportEvidenceRef(report))
	}
	if semanticReceipt == nil {
		return PromotionReview{}, errors.New("promotion requires dedicated candidate semantic verification")
	}
	refs, err = mergeEvidenceRefs(nil, refs)
	if err != nil {
		return PromotionReview{}, err
	}
	procedureReceipt := map[string]any{}
	if source.Kind == "procedure" {
		episodes, signature, err := procedureEpisodeIDs(source.Metadata)
		if err != nil {
			return PromotionReview{}, err
		}
		for _, id := range episodes {
			if id == source.BlockID {
				return PromotionReview{}, errors.New("procedure cannot cite itself as a source episode")
			}
			episode, err := engine.ReadStored(ctx, id)
			if err != nil {
				return PromotionReview{}, err
			}
			if episode == nil ||
				episode.Status == "quarantined" || episode.Status == "superseded" ||
				episode.Status == "deprecated" || episode.Status == "retracted" {
				return PromotionReview{}, errors.New("procedure source episode is unavailable")
			}
			current, err := engine.index.IsCurrentRecord(ctx, *episode)
			if err != nil {
				return PromotionReview{}, err
			}
			if !current {
				return PromotionReview{}, errors.New("procedure source episode is no longer current")
			}
		}
		gate, err := engine.ProcedurePromotionGate(ctx, source.BlockID, signature)
		if err != nil {
			return PromotionReview{}, err
		}
		if !gate.Allowed {
			return PromotionReview{}, errors.New("procedure requires two independent verified applications")
		}
		procedureReceipt["procedure_promotion_gate"] = gate
	}
	edges, err := engine.index.EdgesFor(ctx, source.BlockID)
	if err != nil {
		return PromotionReview{}, err
	}
	correctionTarget, _ := source.Metadata["correction_target_id"].(string)
	filtered := edges[:0]
	for _, edge := range edges {
		if correctionTarget != "" && edge.EdgeType == "contradicts" &&
			edge.SrcID == source.BlockID && edge.DstID == correctionTarget {
			continue
		}
		filtered = append(filtered, edge)
	}
	actor := "verifier"
	decision, err := memory.RunTribunal(memory.TribunalInput{CandidateID: source.BlockID,
		Rules: []memory.TribunalRule{{Status: source.Status}}, SourceEvidence: refs,
		Actor: &actor, SourceBackend: source.SourceBackend, WorkspaceID: source.WorkspaceID,
		TargetWorkspaceID: engine.config.WorkspaceID, GraphEdges: filtered})
	if err != nil {
		return PromotionReview{}, err
	}
	if decision.Decision != "accept" {
		return PromotionReview{}, fmt.Errorf("promotion tribunal %s: %s", decision.Decision, decision.Reason)
	}
	return PromotionReview{Source: *source, Subject: subject,
		SemanticReceipt: semanticReceipt, ProcedureGateReceipt: procedureReceipt,
		PromotionRefs: refs, Decision: decision}, nil
}
