package memory

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
)

type ConsolidationShadowView struct {
	ViewID  string `json:"view_id"`
	Payload string `json:"payload"`
}

type ConsolidationShadowSnapshot struct {
	SnapshotID      string                    `json:"snapshot_id"`
	WorkspaceID     string                    `json:"workspace_id"`
	TransitionID    string                    `json:"transition_id"`
	CandidateSHA256 string                    `json:"candidate_sha256"`
	ViewPayloads    []ConsolidationShadowView `json:"view_payloads"`
}

type ConsolidationRejectionReceipt struct {
	ReceiptID       string        `json:"receipt_id"`
	WorkspaceID     string        `json:"workspace_id"`
	TransitionID    string        `json:"transition_id"`
	CandidateSHA256 string        `json:"candidate_sha256"`
	Disposition     string        `json:"disposition"`
	VerifierReasons []string      `json:"verifier_reasons"`
	EvidenceRefs    []EvidenceRef `json:"evidence_refs"`
}

type AtomicConsolidationCommitReceipt struct {
	ReceiptID          string        `json:"receipt_id"`
	WorkspaceID        string        `json:"workspace_id"`
	TransitionID       string        `json:"transition_id"`
	PreviousHeadSHA256 string        `json:"previous_head_sha256"`
	ActiveHeadSHA256   string        `json:"active_head_sha256"`
	CommittedViewIDs   []string      `json:"committed_view_ids"`
	EvidenceRefs       []EvidenceRef `json:"evidence_refs"`
}

type ConsolidationShadowResolution struct {
	SnapshotID       string                            `json:"snapshot_id"`
	Disposition      string                            `json:"disposition"`
	CommitReceipt    *AtomicConsolidationCommitReceipt `json:"commit_receipt,omitempty"`
	RejectionReceipt *ConsolidationRejectionReceipt    `json:"rejection_receipt,omitempty"`
}

type pendingConsolidation struct {
	snapshot   ConsolidationShadowSnapshot
	transition ConsolidationTransition
}

type activeConsolidation struct {
	head  string
	views map[string]string
}

// ConsolidationShadowStore isolates candidates until a tribunal report is
// resolved. The active view state is in memory; durable canonical writes still
// require the later evidence gate and repository commit stage.
type ConsolidationShadowStore struct {
	mu                  sync.Mutex
	pendingByWorkspace  map[string]pendingConsolidation
	pendingBySnapshot   map[string]string
	active              map[string]activeConsolidation
	rejectedByWorkspace map[string]map[string]bool
}

func NewConsolidationShadowStore() *ConsolidationShadowStore {
	return &ConsolidationShadowStore{
		pendingByWorkspace:  map[string]pendingConsolidation{},
		pendingBySnapshot:   map[string]string{},
		active:              map[string]activeConsolidation{},
		rejectedByWorkspace: map[string]map[string]bool{},
	}
}

func (store *ConsolidationShadowStore) Stage(workspaceID string,
	transition ConsolidationTransition) (ConsolidationShadowSnapshot, error) {
	result := ConsolidationShadowSnapshot{}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return result, errors.New("workspace_id is required")
	}
	if err := transition.Validate(); err != nil {
		return result, err
	}
	var candidate map[string]any
	decoder := json.NewDecoder(strings.NewReader(transition.PostStateCandidate))
	decoder.UseNumber()
	if err := decoder.Decode(&candidate); err != nil || candidate == nil || candidate["workspace_id"] != workspaceID {
		return result, errors.New("shadow candidate workspace mismatch")
	}
	views := []ConsolidationShadowView{}
	if raw, present := candidate["materialized_views"]; present {
		items, ok := raw.([]any)
		if !ok {
			return result, errors.New("materialized_views must be a list")
		}
		for _, item := range items {
			object, ok := item.(map[string]any)
			if !ok {
				return result, errors.New("materialized view payload must be an object")
			}
			viewID, ok := object["view_id"].(string)
			if !ok || strings.TrimSpace(viewID) == "" {
				return result, errors.New("view_id is required")
			}
			payload, err := jsoncompat.MarshalPython(object, false)
			if err != nil {
				return result, err
			}
			views = append(views, ConsolidationShadowView{ViewID: strings.TrimSpace(viewID), Payload: string(payload)})
		}
	}
	slices.SortFunc(views, func(a, b ConsolidationShadowView) int { return strings.Compare(a.ViewID, b.ViewID) })
	for i := 1; i < len(views); i++ {
		if views[i].ViewID == views[i-1].ViewID {
			return result, errors.New("materialized view IDs must be unique")
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.pendingByWorkspace[workspaceID]; exists {
		return result, errors.New("only one bounded candidate may be staged per workspace")
	}
	if store.rejectedByWorkspace[workspaceID][transition.PostStateCandidateSHA256] {
		return result, errors.New("previously rejected candidate revision cannot be staged again")
	}
	result = ConsolidationShadowSnapshot{SnapshotID: "shadow-" + transition.TransitionID,
		WorkspaceID: workspaceID, TransitionID: transition.TransitionID,
		CandidateSHA256: transition.PostStateCandidateSHA256, ViewPayloads: views}
	// A defensive copy prevents the caller changing staged transition slices.
	encoded, err := json.Marshal(transition)
	if err != nil {
		return ConsolidationShadowSnapshot{}, err
	}
	var frozen ConsolidationTransition
	if err := json.Unmarshal(encoded, &frozen); err != nil {
		return ConsolidationShadowSnapshot{}, err
	}
	frozenSnapshot := result
	frozenSnapshot.ViewPayloads = slices.Clone(result.ViewPayloads)
	store.pendingByWorkspace[workspaceID] = pendingConsolidation{snapshot: frozenSnapshot, transition: frozen}
	store.pendingBySnapshot[result.SnapshotID] = workspaceID
	return result, nil
}

func (store *ConsolidationShadowStore) Resolve(snapshotID string, report ConsolidationTribunalReport,
	quarantineOnReject bool) (ConsolidationShadowResolution, error) {
	result := ConsolidationShadowResolution{}
	snapshotID = strings.TrimSpace(snapshotID)
	if snapshotID == "" {
		return result, errors.New("snapshot_id is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	workspaceID, found := store.pendingBySnapshot[snapshotID]
	if !found {
		return result, errors.New("shadow snapshot is not pending")
	}
	pending := store.pendingByWorkspace[workspaceID]
	snapshot := pending.snapshot
	transition := pending.transition
	if snapshot.SnapshotID != snapshotID {
		return result, errors.New("shadow snapshot identity mismatch")
	}
	if report.TransitionID != transition.TransitionID ||
		report.ReportID != "tribunal-"+transition.TransitionID ||
		report.Assessment.TransitionID != transition.TransitionID ||
		report.Assessment.DecisionID != transition.DecisionID ||
		report.Assessment.PreStateSnapshotSHA256 != transition.PreStateSnapshotSHA256 ||
		report.Assessment.PostStateCandidateSHA256 != snapshot.CandidateSHA256 ||
		!reflect.DeepEqual(report.Assessment.SourceCoverageDiff, transition.SourceCoverageDiff) ||
		report.Assessment.PolicyVersion != transition.PolicyVersion ||
		report.Assessment.ModelVersion != transition.ModelVersion ||
		report.Assessment.PromptVersion != transition.PromptVersion ||
		report.Assessment.Verdict != report.Verdict ||
		report.CanonicalMutationApplied || report.HostActionAuthorized ||
		report.Assessment.CanonicalMutationApplied || report.Assessment.HostActionAuthorized ||
		(report.Verdict != "accept" && report.Verdict != "reject") {
		return result, errors.New("tribunal report does not match the staged candidate")
	}
	expectedChecks := []string{"origin_integrity", "no_authority_uplift", "scope_validity_lifecycle",
		"source_coverage", "rare_exact_literals", "contradiction_correction_closure",
		"privacy_erase_closure", "affected_materialized_views", "continuation_applicability",
		"procedure_applicability", "independent_semantic_verification"}
	if len(report.DeterministicChecks) != len(expectedChecks) ||
		len(report.Assessment.Reasons) != len(expectedChecks) {
		return result, errors.New("tribunal report is missing invariant checks")
	}
	for i, check := range report.DeterministicChecks {
		if check.CheckID != expectedChecks[i] || check.Reason != report.Assessment.Reasons[i] {
			return result, errors.New("tribunal report invariant checks do not match assessment")
		}
	}
	if err := report.SemanticVerification.Validate(); err != nil {
		return result, err
	}
	if _, err := uniqueEvidenceRefs(report.Assessment.EvidenceRefs, true); err != nil {
		return result, err
	}
	delete(store.pendingBySnapshot, snapshotID)
	delete(store.pendingByWorkspace, workspaceID)
	if report.Verdict == "accept" && len(report.FailedCheckIDs()) == 0 {
		previous := store.active[workspaceID].head
		views := make(map[string]string, len(snapshot.ViewPayloads))
		ids := make([]string, 0, len(snapshot.ViewPayloads))
		for _, view := range snapshot.ViewPayloads {
			views[view.ViewID] = view.Payload
			ids = append(ids, view.ViewID)
		}
		store.active[workspaceID] = activeConsolidation{head: snapshot.CandidateSHA256, views: views}
		receipt := &AtomicConsolidationCommitReceipt{ReceiptID: "commit-" + transition.TransitionID,
			WorkspaceID: workspaceID, TransitionID: transition.TransitionID,
			PreviousHeadSHA256: previous, ActiveHeadSHA256: snapshot.CandidateSHA256,
			CommittedViewIDs: ids, EvidenceRefs: slices.Clone(report.Assessment.EvidenceRefs)}
		return ConsolidationShadowResolution{SnapshotID: snapshotID, Disposition: "committed", CommitReceipt: receipt}, nil
	}
	if store.rejectedByWorkspace[workspaceID] == nil {
		store.rejectedByWorkspace[workspaceID] = map[string]bool{}
	}
	store.rejectedByWorkspace[workspaceID][snapshot.CandidateSHA256] = true
	disposition := "rejected"
	if quarantineOnReject {
		disposition = "quarantined"
	}
	receipt := &ConsolidationRejectionReceipt{ReceiptID: "reject-" + transition.TransitionID,
		WorkspaceID: workspaceID, TransitionID: transition.TransitionID,
		CandidateSHA256: snapshot.CandidateSHA256, Disposition: disposition,
		VerifierReasons: slices.Clone(report.Assessment.Reasons),
		EvidenceRefs:    slices.Clone(report.Assessment.EvidenceRefs)}
	return ConsolidationShadowResolution{SnapshotID: snapshotID, Disposition: disposition,
		RejectionReceipt: receipt}, nil
}

func (store *ConsolidationShadowStore) ActiveHead(workspaceID string) string {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.active[workspaceID].head
}

func (store *ConsolidationShadowStore) ActiveViews(workspaceID string) map[string]string {
	store.mu.Lock()
	defer store.mu.Unlock()
	views := store.active[workspaceID].views
	result := make(map[string]string, len(views))
	for id, payload := range views {
		result[id] = payload
	}
	return result
}
