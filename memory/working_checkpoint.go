package memory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"golang.org/x/text/unicode/norm"
)

type ContinuationLiteralSet struct {
	Dimension string   `json:"dimension"`
	Literals  []string `json:"literals"`
}

type RejectedApproach struct {
	Approach string `json:"approach"`
	Reason   string `json:"reason"`
}

type WorkingStateCheckpoint struct {
	WorkspaceID                   string                   `json:"workspace_id"`
	ProfileID                     string                   `json:"profile_id"`
	OwnerGeneration               string                   `json:"owner_generation"`
	RequestedBlockID              string                   `json:"requested_block_id"`
	WorkingView                   string                   `json:"working_view"`
	SourceRefSHA256               [][2]string              `json:"source_ref_sha256"`
	HostEventID                   string                   `json:"host_event_id"`
	SessionID                     string                   `json:"session_id"`
	TaskID                        string                   `json:"task_id"`
	SourceEventRefs               []string                 `json:"source_event_refs"`
	SourceEventSHA256             string                   `json:"source_event_sha256"`
	TaskAnchorRefs                []string                 `json:"task_anchor_refs"`
	SystemAnchorRefs              []string                 `json:"system_anchor_refs"`
	ExactRecentTailRefs           []string                 `json:"exact_recent_tail_refs"`
	EvidencePageRefs              [][]string               `json:"evidence_page_refs"`
	RollbackOrPageBackRef         string                   `json:"rollback_or_page_back_ref"`
	ValidFrom                     string                   `json:"valid_from"`
	ValidTo                       string                   `json:"valid_to"`
	SchemaVersion                 int                      `json:"schema_version"`
	HardPolicyVersion             int                      `json:"hard_policy_version"`
	SemanticCatalogVersion        string                   `json:"semantic_catalog_version"`
	HostVersion                   string                   `json:"host_version"`
	ModelVersion                  string                   `json:"model_version"`
	PromptVersion                 string                   `json:"prompt_version"`
	ContinuationSufficiency       string                   `json:"continuation_sufficiency"`
	EvidenceRefs                  []EvidenceRef            `json:"evidence_refs"`
	ExactLiteralManifest          []ContinuationLiteralSet `json:"exact_literal_manifest"`
	SummarySourceRefs             []string                 `json:"summary_source_refs"`
	TransitiveRawRefs             []string                 `json:"transitive_raw_refs"`
	PreservationVerified          bool                     `json:"preservation_verified"`
	RepositoryID                  string                   `json:"repository_id"`
	Checkout                      string                   `json:"checkout"`
	Worktree                      string                   `json:"worktree"`
	Branch                        string                   `json:"branch"`
	Revision                      string                   `json:"revision"`
	DirtyState                    string                   `json:"dirty_state"`
	TouchedFiles                  []string                 `json:"touched_files"`
	TouchedSymbols                []string                 `json:"touched_symbols"`
	CurrentDiff                   string                   `json:"current_diff"`
	Tests                         []string                 `json:"tests"`
	Errors                        []string                 `json:"errors"`
	ParentSessionID               string                   `json:"parent_session_id"`
	ArtifactID                    string                   `json:"artifact_id"`
	ArtifactRevision              string                   `json:"artifact_revision"`
	ProjectID                     string                   `json:"project_id"`
	CurrentGoal                   string                   `json:"current_goal"`
	Plan                          []string                 `json:"plan"`
	CurrentSubgoal                string                   `json:"current_subgoal"`
	UnresolvedConstraints         []string                 `json:"unresolved_constraints"`
	OpenHypotheses                []string                 `json:"open_hypotheses"`
	VerifiedHypotheses            []string                 `json:"verified_hypotheses"`
	RejectedApproaches            []RejectedApproach       `json:"rejected_approaches"`
	SafetyState                   []string                 `json:"safety_state"`
	OpenBranchIDs                 []string                 `json:"open_branch_ids"`
	CompletedBranchIDs            []string                 `json:"completed_branch_ids"`
	IsolatedBranchIDs             []string                 `json:"isolated_branch_ids"`
	PendingProspectiveObligations []string                 `json:"pending_prospective_obligations"`
	RemainingWork                 []string                 `json:"remaining_work"`
}

func (checkpoint *WorkingStateCheckpoint) UnmarshalJSON(payload []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return errors.New("working checkpoint must be a JSON object")
	}
	arrayFields := []string{"source_ref_sha256", "source_event_refs", "task_anchor_refs",
		"system_anchor_refs", "exact_recent_tail_refs", "evidence_page_refs", "evidence_refs",
		"exact_literal_manifest", "summary_source_refs", "transitive_raw_refs", "touched_files",
		"touched_symbols", "tests", "errors", "plan", "unresolved_constraints",
		"open_hypotheses", "verified_hypotheses", "rejected_approaches", "safety_state",
		"open_branch_ids", "completed_branch_ids", "isolated_branch_ids",
		"pending_prospective_obligations", "remaining_work"}
	for _, field := range arrayFields {
		if raw, exists := fields[field]; exists {
			trimmed := bytes.TrimSpace(raw)
			if len(trimmed) == 0 || trimmed[0] != '[' {
				return fmt.Errorf("working checkpoint %s requires an array", field)
			}
		}
	}
	for _, field := range []string{"source_ref_sha256", "evidence_page_refs"} {
		raw, exists := fields[field]
		if !exists {
			continue
		}
		var nested []json.RawMessage
		if err := json.Unmarshal(raw, &nested); err != nil {
			return err
		}
		for _, item := range nested {
			trimmed := bytes.TrimSpace(item)
			if len(trimmed) == 0 || trimmed[0] != '[' {
				return fmt.Errorf("working checkpoint %s requires an array of arrays", field)
			}
			if field == "source_ref_sha256" {
				var pair []json.RawMessage
				if err := json.Unmarshal(item, &pair); err != nil || len(pair) != 2 {
					return errors.New("source_ref_sha256 requires exact reference/hash pairs")
				}
			}
		}
	}
	type checkpointAlias WorkingStateCheckpoint
	var decoded checkpointAlias
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*checkpoint = WorkingStateCheckpoint(decoded)
	return nil
}

func validCheckpointHash(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func checkpointTexts(values []string, required bool) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return nil, errors.New("working checkpoint references require nonempty text")
		}
		value = strings.TrimSpace(value)
		if !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
	}
	if required && len(result) == 0 {
		return nil, errors.New("working checkpoint requires exact references")
	}
	return result, nil
}

func normalizeCheckpointPath(value string) (string, error) {
	value = norm.NFC.String(strings.TrimSpace(value))
	if value == "" {
		return "", nil
	}
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, strings.TrimLeft(value[1:], `/\`))
	}
	resolved, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = real
	}
	resolved = norm.NFC.String(filepath.Clean(resolved))
	if runtime.GOOS == "windows" {
		resolved = strings.ToLower(resolved)
	}
	return resolved, nil
}

func checkpointManifest(checkpoint WorkingStateCheckpoint) []ContinuationLiteralSet {
	makeSet := func(dimension string, items ...string) ContinuationLiteralSet {
		literals, _ := checkpointTexts(items, false)
		return ContinuationLiteralSet{Dimension: dimension, Literals: literals}
	}
	goals := []string{checkpoint.WorkingView}
	if checkpoint.CurrentGoal != "" {
		goals = append(goals, checkpoint.CurrentGoal)
	}
	if checkpoint.CurrentSubgoal != "" {
		goals = append(goals, checkpoint.CurrentSubgoal)
	}
	goals = append(goals, checkpoint.Plan...)
	goals = append(goals, checkpoint.UnresolvedConstraints...)
	paths := append(slices.Clone(checkpoint.TouchedFiles), checkpoint.TouchedSymbols...)
	diffs := []string{}
	if checkpoint.CurrentDiff != "" {
		diffs = append(diffs, checkpoint.CurrentDiff)
	}
	rejected := make([]string, 0, len(checkpoint.RejectedApproaches))
	for _, item := range checkpoint.RejectedApproaches {
		rejected = append(rejected, item.Reason)
	}
	raw := append([]string{}, checkpoint.SourceEventRefs...)
	raw = append(raw, checkpoint.TaskAnchorRefs...)
	raw = append(raw, checkpoint.SystemAnchorRefs...)
	raw = append(raw, checkpoint.ExactRecentTailRefs...)
	for _, page := range checkpoint.EvidencePageRefs {
		raw = append(raw, page...)
	}
	raw = append(raw, checkpoint.TransitiveRawRefs...)
	raw = append(raw, checkpoint.RollbackOrPageBackRef)
	return []ContinuationLiteralSet{
		makeSet("goals_constraints", goals...), makeSet("paths_symbols", paths...),
		makeSet("diff", diffs...), makeSet("error_literals", checkpoint.Errors...),
		makeSet("tests", checkpoint.Tests...), makeSet("open_hypotheses", checkpoint.OpenHypotheses...),
		makeSet("rejected_reasons", rejected...), makeSet("safety_state", checkpoint.SafetyState...),
		makeSet("raw_page_back_coverage", raw...),
	}
}

// NormalizeWorkingStateCheckpoint validates the Python checkpoint contract and
// materializes every omitted optional tuple as an empty JSON array.
func NormalizeWorkingStateCheckpoint(checkpoint WorkingStateCheckpoint) (WorkingStateCheckpoint, error) {
	for _, field := range []*string{&checkpoint.WorkspaceID, &checkpoint.ProfileID, &checkpoint.OwnerGeneration,
		&checkpoint.RequestedBlockID, &checkpoint.HostEventID, &checkpoint.SessionID, &checkpoint.TaskID,
		&checkpoint.ValidFrom, &checkpoint.ValidTo, &checkpoint.SemanticCatalogVersion,
		&checkpoint.HostVersion, &checkpoint.ModelVersion, &checkpoint.PromptVersion,
		&checkpoint.RollbackOrPageBackRef} {
		*field = strings.TrimSpace(*field)
		if *field == "" {
			return checkpoint, errors.New("working checkpoint requires exact identity and version fields")
		}
	}
	for _, field := range []*string{&checkpoint.ParentSessionID, &checkpoint.ArtifactID,
		&checkpoint.ArtifactRevision, &checkpoint.ProjectID, &checkpoint.RepositoryID,
		&checkpoint.Branch, &checkpoint.Revision, &checkpoint.DirtyState} {
		*field = strings.TrimSpace(*field)
	}
	var err error
	checkpoint.Checkout, err = normalizeCheckpointPath(checkpoint.Checkout)
	if err != nil {
		return checkpoint, err
	}
	checkpoint.Worktree, err = normalizeCheckpointPath(checkpoint.Worktree)
	if err != nil {
		return checkpoint, err
	}
	if checkpoint.ParentSessionID != "" && checkpoint.SessionID == "" {
		return checkpoint, errors.New("parent_session_id requires session_id")
	}
	if strings.TrimSpace(checkpoint.WorkingView) == "" || checkpoint.SchemaVersion < 1 ||
		checkpoint.HardPolicyVersion < 1 || !validCheckpointHash(checkpoint.SourceEventSHA256) {
		return checkpoint, errors.New("working checkpoint has invalid view, policy version or source hash")
	}
	if checkpoint.ContinuationSufficiency != "sufficient" && checkpoint.ContinuationSufficiency != "insufficient" {
		return checkpoint, errors.New("working checkpoint continuation sufficiency is invalid")
	}
	for _, field := range []*[]string{&checkpoint.SourceEventRefs, &checkpoint.TaskAnchorRefs,
		&checkpoint.SystemAnchorRefs, &checkpoint.ExactRecentTailRefs} {
		*field, err = checkpointTexts(*field, true)
		if err != nil {
			return checkpoint, err
		}
	}
	if len(checkpoint.EvidencePageRefs) == 0 {
		return checkpoint, errors.New("working checkpoint requires evidence pages")
	}
	checkpoint.EvidencePageRefs = slices.Clone(checkpoint.EvidencePageRefs)
	for i, page := range checkpoint.EvidencePageRefs {
		checkpoint.EvidencePageRefs[i], err = checkpointTexts(page, true)
		if err != nil {
			return checkpoint, err
		}
	}
	for _, field := range []*[]string{&checkpoint.Plan, &checkpoint.UnresolvedConstraints,
		&checkpoint.TouchedFiles, &checkpoint.TouchedSymbols, &checkpoint.Tests, &checkpoint.Errors,
		&checkpoint.OpenHypotheses, &checkpoint.VerifiedHypotheses, &checkpoint.SafetyState,
		&checkpoint.OpenBranchIDs, &checkpoint.CompletedBranchIDs, &checkpoint.IsolatedBranchIDs,
		&checkpoint.PendingProspectiveObligations, &checkpoint.RemainingWork,
		&checkpoint.SummarySourceRefs, &checkpoint.TransitiveRawRefs} {
		*field, err = checkpointTexts(*field, false)
		if err != nil {
			return checkpoint, err
		}
	}
	for _, rejected := range checkpoint.RejectedApproaches {
		if strings.TrimSpace(rejected.Approach) == "" || strings.TrimSpace(rejected.Reason) == "" {
			return checkpoint, errors.New("working checkpoint rejected approach requires its reason")
		}
	}
	if checkpoint.RejectedApproaches == nil {
		checkpoint.RejectedApproaches = []RejectedApproach{}
	}
	if len(checkpoint.SummarySourceRefs) > 0 &&
		(len(checkpoint.TransitiveRawRefs) == 0 || !checkpoint.PreservationVerified) {
		return checkpoint, errors.New("recursive summary requires verified transitive raw references")
	}
	for _, id := range checkpoint.OpenBranchIDs {
		if slices.Contains(checkpoint.CompletedBranchIDs, id) {
			return checkpoint, errors.New("open and completed branch IDs must be disjoint")
		}
	}
	linked := map[string]bool{}
	for _, group := range [][]string{checkpoint.SourceEventRefs, checkpoint.TaskAnchorRefs,
		checkpoint.SystemAnchorRefs, checkpoint.ExactRecentTailRefs} {
		for _, id := range group {
			linked[id] = true
		}
	}
	for _, page := range checkpoint.EvidencePageRefs {
		for _, id := range page {
			linked[id] = true
		}
	}
	for _, id := range checkpoint.TransitiveRawRefs {
		if !linked[id] {
			return checkpoint, errors.New("transitive raw references must name immutable raw events")
		}
	}
	for _, id := range checkpoint.TransitiveRawRefs {
		linked[id] = true
	}
	if len(checkpoint.SourceRefSHA256) != len(linked) {
		return checkpoint, errors.New("source_ref_sha256 must pin every exact reference once")
	}
	seenHashes := map[string]bool{}
	for _, pair := range checkpoint.SourceRefSHA256 {
		if pair[0] != strings.TrimSpace(pair[0]) || !linked[pair[0]] || seenHashes[pair[0]] || !validCheckpointHash(pair[1]) {
			return checkpoint, errors.New("source_ref_sha256 must pin every exact reference once")
		}
		seenHashes[pair[0]] = true
	}
	expected := checkpointManifest(checkpoint)
	if len(checkpoint.ExactLiteralManifest) > 0 && !reflect.DeepEqual(checkpoint.ExactLiteralManifest, expected) {
		return checkpoint, errors.New("exact_literal_manifest does not match working state")
	}
	checkpoint.ExactLiteralManifest = expected
	if len(checkpoint.EvidenceRefs) == 0 {
		return checkpoint, errors.New("working-state checkpoint requires evidence_refs")
	}
	for _, ref := range checkpoint.EvidenceRefs {
		if err := ref.Validate(); err != nil {
			return checkpoint, err
		}
	}
	return checkpoint, nil
}

func ContinuationSourceHash(record MemoryRecord) (string, error) {
	value := map[string]any{
		"block_id": record.BlockID, "canonical_key": record.CanonicalKey,
		"revision_id": record.RevisionID, "workspace_id": record.WorkspaceID,
		"profile_id": record.ProfileID, "session_id": record.SessionID,
		"task_id": record.TaskID, "content": record.Content,
	}
	digest, err := PythonHashValue(value)
	if err != nil {
		return "", err
	}
	return "sha256:" + digest, nil
}

func ContinuationWindowHash(records []MemoryRecord) (string, error) {
	manifest := make([]map[string]string, 0, len(records))
	for _, record := range records {
		contentHash := sha256.Sum256([]byte(record.Content))
		manifest = append(manifest, map[string]string{
			"block_id": record.BlockID, "canonical_key": record.CanonicalKey,
			"revision_id":    record.RevisionID,
			"content_sha256": hex.EncodeToString(contentHash[:]),
		})
	}
	encoded, err := jsoncompat.MarshalPython(manifest, false)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func CheckpointVerificationSubject(checkpoint WorkingStateCheckpoint) (CandidateVerificationSubject, error) {
	checkpoint, err := NormalizeWorkingStateCheckpoint(checkpoint)
	if err != nil {
		return CandidateVerificationSubject{}, err
	}
	// Marshal via JSON to use the same object and array shape as Python's
	// dataclass-aware json_ready before computing the verifier's payload hash.
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return CandidateVerificationSubject{}, err
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return CandidateVerificationSubject{}, err
	}
	payloadHash, err := PythonHashValue(payload)
	if err != nil {
		return CandidateVerificationSubject{}, err
	}
	evidenceHash, err := PythonHashValue(checkpoint.SourceRefSHA256)
	if err != nil {
		return CandidateVerificationSubject{}, err
	}
	revisionID := checkpoint.ArtifactRevision
	if revisionID == "" {
		revisionID = checkpoint.SourceEventSHA256
	}
	return CandidateVerificationSubject{WorkspaceID: checkpoint.WorkspaceID,
		ProfileID: checkpoint.ProfileID, MemoryID: "host-checkpoint:" + checkpoint.HostEventID,
		RevisionID: revisionID, PayloadHash: payloadHash,
		SourceEvidenceHash: evidenceHash, TargetBlockID: checkpoint.RequestedBlockID,
		Authority: "advisory", Scope: "task", ScopeID: checkpoint.TaskID}, nil
}

type ContinuationPacket struct {
	WorkspaceID             string                   `json:"workspace_id"`
	ProfileID               string                   `json:"profile_id"`
	OwnerGeneration         string                   `json:"owner_generation"`
	SessionID               string                   `json:"session_id"`
	TaskID                  string                   `json:"task_id"`
	Checkpoint              WorkingStateCheckpoint   `json:"checkpoint"`
	SourceEventRange        []string                 `json:"source_event_range"`
	SourceEventSHA256       string                   `json:"source_event_sha256"`
	TaskAnchorRefs          []string                 `json:"task_anchor_refs"`
	SystemAnchorRefs        []string                 `json:"system_anchor_refs"`
	ExactRecentTailRefs     []string                 `json:"exact_recent_tail_refs"`
	EvidencePageRefs        [][]string               `json:"evidence_page_refs"`
	SchemaVersion           int                      `json:"schema_version"`
	HardPolicyVersion       int                      `json:"hard_policy_version"`
	SemanticCatalogVersion  string                   `json:"semantic_catalog_version"`
	HostVersion             string                   `json:"host_version"`
	ModelVersion            string                   `json:"model_version"`
	PromptVersion           string                   `json:"prompt_version"`
	ExactLiteralManifest    []ContinuationLiteralSet `json:"exact_literal_manifest"`
	ContinuationSufficiency string                   `json:"continuation_sufficiency"`
	ExpiresAt               string                   `json:"expires_at"`
	RollbackOrPageBackRef   string                   `json:"rollback_or_page_back_ref"`
}

func BuildContinuationPacket(checkpoint WorkingStateCheckpoint) (ContinuationPacket, error) {
	checkpoint, err := NormalizeWorkingStateCheckpoint(checkpoint)
	if err != nil {
		return ContinuationPacket{}, err
	}
	return ContinuationPacket{WorkspaceID: checkpoint.WorkspaceID,
		ProfileID: checkpoint.ProfileID, OwnerGeneration: checkpoint.OwnerGeneration,
		SessionID: checkpoint.SessionID, TaskID: checkpoint.TaskID, Checkpoint: checkpoint,
		SourceEventRange: checkpoint.SourceEventRefs, SourceEventSHA256: checkpoint.SourceEventSHA256,
		TaskAnchorRefs: checkpoint.TaskAnchorRefs, SystemAnchorRefs: checkpoint.SystemAnchorRefs,
		ExactRecentTailRefs: checkpoint.ExactRecentTailRefs, EvidencePageRefs: checkpoint.EvidencePageRefs,
		SchemaVersion: checkpoint.SchemaVersion, HardPolicyVersion: checkpoint.HardPolicyVersion,
		SemanticCatalogVersion: checkpoint.SemanticCatalogVersion, HostVersion: checkpoint.HostVersion,
		ModelVersion: checkpoint.ModelVersion, PromptVersion: checkpoint.PromptVersion,
		ExactLiteralManifest:    checkpoint.ExactLiteralManifest,
		ContinuationSufficiency: checkpoint.ContinuationSufficiency, ExpiresAt: checkpoint.ValidTo,
		RollbackOrPageBackRef: checkpoint.RollbackOrPageBackRef}, nil
}

func WorkingStateCanonicalKey(workspaceID, profileID, ownerGeneration, sessionID, taskID string) (string, error) {
	values := []string{workspaceID, profileID, ownerGeneration, sessionID, taskID}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return "", errors.New("working state requires exact scope identities")
		}
	}
	encoded, err := jsoncompat.MarshalPython(values, false)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return fmt.Sprintf("%s:task_state:working_continuity:%s", workspaceID, hex.EncodeToString(hash[:])[:24]), nil
}
