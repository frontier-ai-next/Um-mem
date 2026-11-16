package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/receipts"
	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
)

// CandidateRequest covers the standard agent-authored write path. Lossless
// source registration is a separate contract and is not accepted here.
type CandidateRequest struct {
	OperationID              string               `json:"operation_id"`
	Operation                string               `json:"operation,omitempty"`
	RepairOf                 string               `json:"repair_of,omitempty"`
	WriteMode                string               `json:"write_mode,omitempty"`
	Content                  string               `json:"content"`
	Title                    string               `json:"title"`
	Kind                     string               `json:"kind"`
	Tags                     []string             `json:"tags"`
	Metadata                 map[string]any       `json:"metadata"`
	EvidenceRefs             []memory.EvidenceRef `json:"evidence_refs"`
	RequestedBlockID         string               `json:"requested_block_id"`
	SourceID                 string               `json:"source_id"`
	EvidenceKind             string               `json:"evidence_kind"`
	UserID                   string               `json:"user_id"`
	ProjectID                string               `json:"project_id"`
	SessionID                string               `json:"session_id"`
	TaskID                   string               `json:"task_id"`
	SubtaskID                string               `json:"subtask_id"`
	ParentSessionID          string               `json:"parent_session_id"`
	RunID                    string               `json:"run_id"`
	StageID                  string               `json:"stage_id"`
	ArtifactID               string               `json:"artifact_id"`
	ArtifactRevision         string               `json:"artifact_revision"`
	ValidFrom                string               `json:"valid_from"`
	ValidTo                  string               `json:"valid_to"`
	RepositoryID             string               `json:"repository_id"`
	Checkout                 string               `json:"checkout"`
	Worktree                 string               `json:"worktree"`
	Branch                   string               `json:"branch"`
	Revision                 string               `json:"revision"`
	DirtyState               string               `json:"dirty_state"`
	Sensitivity              string               `json:"sensitivity"`
	ContentMode              string               `json:"content_mode"`
	OperationalRisk          string               `json:"operational_risk"`
	operationCallerRole      string               `json:"-"`
	operationProtocolVersion int                  `json:"-"`
	operationFingerprint     string               `json:"-"`
	proposalCanonicalKey     string               `json:"-"`
	proposalEvidenceRefs     []memory.EvidenceRef `json:"-"`
}

var losslessProofMetadata = map[string]bool{
	"memory_mode": true, "source_segments": true, "summary_count": true,
	"source_dates": true, "source_speakers": true, "source_bytes": true,
	"stored_bytes": true, "exact_source_coverage": true,
	"source_coverage_status": true, "exact_segment_fidelity": true,
	"verification_scope": true,
}

type CandidateResult struct {
	Saved           bool                 `json:"saved"`
	RecordID        string               `json:"record_id"`
	OperationID     string               `json:"operation_id"`
	Replayed        bool                 `json:"replayed"`
	Route           memory.RouteDecision `json:"route_decision"`
	Receipt         *store.WriteReceipt  `json:"receipt,omitempty"`
	ProvenanceEdges []map[string]any     `json:"provenance_edges,omitempty"`
}

func firstLineTitle(content string) string {
	line := strings.SplitN(strings.TrimSpace(content), "\n", 2)[0]
	line = strings.TrimSuffix(line, "\r")
	runes := []rune(line)
	if len(runes) > 96 {
		line = string(runes[:96])
	}
	return line
}

func defaultValue(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// RememberCandidate routes and commits one standard advisory candidate using
// the selected native backend's durable journal. No caller-supplied record ID
// or governance status can override the routed policy.
func (engine *Engine) RememberCandidate(ctx context.Context, request CandidateRequest) (CandidateResult, error) {
	return engine.RememberCandidateFor(ctx, request, "go_cli", 2)
}

// RememberCandidateFor binds the durable operation receipt to the transport's
// authenticated role. A service caller cannot replay another role's write.
func (engine *Engine) RememberCandidateFor(ctx context.Context, request CandidateRequest, callerRole string, protocolVersion int) (CandidateResult, error) {
	if err := engine.RefreshCatalog(); err != nil {
		return CandidateResult{}, err
	}
	if request.Content == "" || (request.WriteMode != "lossless_atomic" && strings.TrimSpace(request.Content) == "") {
		return CandidateResult{}, errors.New("candidate content is required")
	}
	if request.WriteMode != "" && request.WriteMode != "standard" && request.WriteMode != "lossless_atomic" {
		return CandidateResult{}, errors.New("write_mode must be standard or lossless_atomic")
	}
	operation := defaultValue(request.Operation, "add")
	if operation != "add" && operation != "repair" {
		return CandidateResult{}, errors.New("candidate write operation must be add or repair")
	}
	if operation == "repair" {
		if request.RepairOf == "" || request.RepairOf != strings.TrimSpace(request.RepairOf) {
			return CandidateResult{}, errors.New("repair operation requires exact repair_of")
		}
		matched := false
		for _, ref := range request.EvidenceRefs {
			matched = matched || ref.RefID == request.RepairOf
		}
		if !matched {
			return CandidateResult{}, errors.New("repair_of must match an evidence ref id")
		}
	} else if request.RepairOf != "" {
		return CandidateResult{}, errors.New("add operation forbids repair_of")
	}
	if callerRole == "" || protocolVersion < 1 {
		return CandidateResult{}, errors.New("candidate write requires authenticated caller identity")
	}
	if request.OperationID == "" {
		return engine.rememberCandidateOnce(ctx, request, "", false)
	}
	if request.OperationID != strings.TrimSpace(request.OperationID) || len(request.OperationID) > 256 {
		return CandidateResult{}, errors.New("operation_id must be a bounded exact string")
	}
	identity := receipts.Identity{WorkspaceID: engine.config.WorkspaceID,
		ProfileID: engine.config.ProfileID, Backend: engine.config.BackendID,
		CallerRole: callerRole, ProtocolVersion: protocolVersion}
	fingerprintRequest := request
	fingerprintRequest.OperationID = ""
	arguments := map[string]any{"tool": "remember-candidate", "request": fingerprintRequest}
	operationFingerprint, err := receipts.Fingerprint(arguments)
	if err != nil {
		return CandidateResult{}, err
	}
	request.operationCallerRole = callerRole
	request.operationProtocolVersion = protocolVersion
	request.operationFingerprint = operationFingerprint
	claimed, err := receipts.Claim(ctx, engine.config.RepoRoot, identity, request.OperationID, arguments)
	if err != nil {
		return CandidateResult{}, err
	}
	if claimed.Status == "committed" {
		if claimed.Response["route_rejected"] == true {
			encoded, err := json.Marshal(claimed.Response["route_decision"])
			if err != nil {
				return CandidateResult{}, err
			}
			var route memory.RouteDecision
			if err := json.Unmarshal(encoded, &route); err != nil || route.Accepted || route.Reason == "" {
				return CandidateResult{}, errors.New("candidate operation rejection receipt is invalid")
			}
			return CandidateResult{Saved: false, OperationID: request.OperationID, Replayed: true, Route: route}, nil
		}
		inner, ok := claimed.Response["response"].(map[string]any)
		if claimed.Response["receipt_kind"] != "mutation_response" ||
			claimed.Response["mutation_tool"] != "remember" || !ok || inner["ok"] != true {
			return CandidateResult{}, errors.New("candidate operation receipt response is invalid")
		}
		encoded, err := json.Marshal(inner["result"])
		if err != nil {
			return CandidateResult{}, err
		}
		var result CandidateResult
		if err := json.Unmarshal(encoded, &result); err != nil || result.OperationID != request.OperationID {
			return CandidateResult{}, errors.New("candidate operation receipt result is invalid")
		}
		if result.Saved {
			record, err := engine.ReadStored(ctx, result.RecordID)
			if err != nil || record == nil {
				return CandidateResult{}, errors.New("candidate operation receipt canonical record is unavailable")
			}
		}
		result.Replayed = true
		return result, nil
	}
	result, err := engine.rememberCandidateOnce(ctx, request, claimed.CreatedAt, claimed.Status == "pending")
	if err != nil {
		checksum := sha256.Sum256([]byte(err.Error()))
		failure := map[string]any{"ok": false, "error_type": "GoError",
			"reason_code": "candidate_write_failed", "error_sha256": hex.EncodeToString(checksum[:])}
		if completeErr := receipts.Complete(ctx, engine.config.RepoRoot, identity, request.OperationID, arguments, failure, "failed"); completeErr != nil {
			return CandidateResult{}, errors.Join(err, completeErr)
		}
		return CandidateResult{}, err
	}
	var response map[string]any
	if result.Saved {
		response = map[string]any{"receipt_kind": "mutation_response", "mutation_tool": "remember",
			"response": map[string]any{"ok": true, "result": result}}
	} else {
		response = map[string]any{"ok": false, "saved": false, "route_rejected": true,
			"reason": result.Route.Reason, "backend": engine.config.BackendID,
			"workspace_id": engine.config.WorkspaceID, "route_decision": result.Route}
	}
	if err := receipts.Complete(ctx, engine.config.RepoRoot, identity, request.OperationID, arguments, response, "committed"); err != nil {
		return CandidateResult{}, err
	}
	return result, nil
}

func (engine *Engine) rememberCandidateOnce(ctx context.Context, request CandidateRequest, claimedAt string, replayed bool) (CandidateResult, error) {
	if request.OperationID != "" && (request.operationCallerRole == "" ||
		request.operationProtocolVersion < 1 || len(request.operationFingerprint) != 64) {
		return CandidateResult{}, errors.New("candidate operation has no receipt request binding")
	}
	if replayed && request.OperationID != "" && request.WriteMode == "lossless_atomic" {
		prior, found, err := engine.recoverCommittedLosslessOperation(ctx, request)
		if err != nil || found {
			return prior, err
		}
	}
	if replayed && request.OperationID != "" {
		prior, found, err := engine.recoverCandidateOperation(ctx, request)
		if err != nil || found {
			return prior, err
		}
	}
	for key := range request.Metadata {
		if request.proposalCanonicalKey == "" && (key == "pending_host_approval" ||
			key == "correction_target_id" || key == "correction_target_revision_id") {
			return CandidateResult{}, errors.New("correction proposal metadata is writer-owned")
		}
		if key == "operation_caller_role" || key == "operation_protocol_version" ||
			key == "operation_request_fingerprint" {
			return CandidateResult{}, errors.New("operation metadata is writer-owned")
		}
		if losslessProofMetadata[key] && !(request.WriteMode == "lossless_atomic" && key == "source_segments") {
			return CandidateResult{}, errors.New("lossless source proof requires the validated lossless_atomic write path")
		}
		if key == "repair_of" {
			return CandidateResult{}, errors.New("repair_of is controlled by the typed repair operation")
		}
	}
	if request.Operation == "repair" {
		metadata := make(map[string]any, len(request.Metadata)+1)
		for key, value := range request.Metadata {
			metadata[key] = value
		}
		metadata["repair_of"] = request.RepairOf
		request.Metadata = metadata
	}
	writeMode := defaultValue(request.WriteMode, "standard")
	var proof losslessProof
	var err error
	if writeMode == "lossless_atomic" {
		proof, err = engine.prepareLossless(ctx, request)
		if err != nil {
			return CandidateResult{}, err
		}
		request.Metadata = proof.Metadata
	}
	kind := defaultValue(request.Kind, "observation")
	proposal := memory.WriteProposal{Title: defaultValue(request.Title, firstLineTitle(request.Content)),
		Content: request.Content, Kind: kind, Tags: request.Tags, WorkspaceID: engine.config.WorkspaceID,
		RunID: request.RunID, StageID: request.StageID, SubtaskID: request.SubtaskID,
		SourceID: request.SourceID, EvidenceKind: request.EvidenceKind,
		WriteMode: writeMode, RequestedBlockID: request.RequestedBlockID,
		Metadata: request.Metadata}
	route, err := memory.RouteMemoryWrite(proposal, engine.Blocks())
	if err != nil {
		return CandidateResult{}, err
	}
	if !route.Accepted {
		return CandidateResult{Saved: false, OperationID: request.OperationID, Replayed: replayed, Route: route}, nil
	}
	if proof.Registered {
		request.EvidenceRefs, err = engine.losslessEvidence(ctx, request, proof, route, kind)
		if err != nil {
			return CandidateResult{}, err
		}
	}
	evidenceToValidate := request.EvidenceRefs
	if request.proposalCanonicalKey != "" {
		evidenceToValidate = request.proposalEvidenceRefs
	}
	rawWitnessIDs, err := engine.validateRawWitnessEvidence(ctx, evidenceToValidate,
		request.SessionID, request.ProjectID)
	if err != nil {
		return CandidateResult{}, err
	}
	record, err := memory.NewMemoryRecord(request.Content, engine.config.WorkspaceID)
	if err != nil {
		return CandidateResult{}, err
	}
	if request.OperationID != "" {
		digest := sha256.Sum256([]byte(request.OperationID))
		record.BlockID = "operation_" + hex.EncodeToString(digest[:])
		stableTime := strings.Replace(claimedAt, "+00:00", "Z", 1)
		if _, err := time.Parse(time.RFC3339Nano, stableTime); err != nil {
			return CandidateResult{}, errors.New("candidate operation receipt timestamp is invalid")
		}
		record.CreatedAt, record.UpdatedAt = stableTime, stableTime
	}
	record.ProfileID, record.Title, record.Kind = engine.config.ProfileID, proposal.Title, kind
	record.Status, record.Retention, record.DeliveryPolicy = route.Status, route.Retention, route.DeliveryPolicy
	record.Temperature, record.Authority, record.TrustLevel = route.Temperature, route.Authority, route.TrustLevel
	record.Scope, record.VerificationStatus = route.Scope, route.VerificationStatus
	record.VerificationMethods, record.PromotionDecision = route.VerificationMethods, route.PromotionDecision
	if proof.Registered {
		record.Status = "accepted"
		record.VerificationStatus = "evidence_backed"
		record.VerificationMethods = []string{"artifact_inspection"}
		record.PromotionDecision = "accepted"
	}
	record.Tags, record.EvidenceRefs = route.Tags, append([]memory.EvidenceRef{}, request.EvidenceRefs...)
	record.UserID, record.ProjectID, record.SessionID = request.UserID, request.ProjectID, request.SessionID
	record.TaskID, record.SubtaskID, record.ParentSessionID = request.TaskID, request.SubtaskID, request.ParentSessionID
	record.RunID, record.StageID = request.RunID, request.StageID
	record.ArtifactID, record.ArtifactRevision = request.ArtifactID, request.ArtifactRevision
	record.ValidFrom, record.ValidTo = request.ValidFrom, request.ValidTo
	record.RepositoryID, record.Checkout, record.Worktree = request.RepositoryID, request.Checkout, request.Worktree
	record.Branch, record.Revision, record.DirtyState = request.Branch, request.Revision, request.DirtyState
	record.Sensitivity = defaultValue(request.Sensitivity, "internal")
	record.ContentMode = defaultValue(request.ContentMode, "full")
	record.OperationalRisk = defaultValue(request.OperationalRisk, "low")
	record.SourceBackend = "um_mem"
	record.Metadata = make(map[string]any, len(request.Metadata)+4)
	for key, value := range request.Metadata {
		record.Metadata[key] = value
	}
	record.Metadata["route_decision_reason"] = route.Reason
	record.Metadata["write_mode"] = writeMode
	if proof.Registered {
		record.Metadata["verification_scope"] = "exact_source_fidelity"
	}
	if request.OperationID != "" {
		record.Metadata["operation_id"] = request.OperationID
		record.Metadata["operation_caller_role"] = request.operationCallerRole
		record.Metadata["operation_protocol_version"] = request.operationProtocolVersion
		record.Metadata["operation_request_fingerprint"] = request.operationFingerprint
	}
	if request.SourceID != "" {
		record.Metadata["source_id"] = request.SourceID
	}
	if request.EvidenceKind != "" {
		record.Metadata["evidence_kind"] = request.EvidenceKind
	}
	if _, err := record.ScopeKey(); err != nil {
		return CandidateResult{}, err
	}
	partition, err := engine.blockPartition(ctx, route.BlockID)
	if err != nil {
		return CandidateResult{}, err
	}
	record.BlockMembership = &memory.RecordBlockMembership{PrimaryBlockID: route.BlockID,
		SecondaryBlockIDs: []string{}, CatalogRevisionID: route.CatalogRevisionID,
		BackendPartitionID: partition}
	if claimedAt != "" {
		record.RecordedAt = strings.Replace(claimedAt, "+00:00", "Z", 1)
	} else {
		record.RecordedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
	}
	record.DuplicateClusterID, err = memory.DuplicateClusterID(record)
	if err != nil {
		return CandidateResult{}, err
	}
	if request.proposalCanonicalKey != "" {
		record.CanonicalKey = request.proposalCanonicalKey
	} else {
		record.CanonicalKey, err = memory.DeriveCanonicalKey(record.WorkspaceID, record.ProfileID, record.Kind, record.BlockID)
		if err != nil {
			return CandidateResult{}, err
		}
	}
	record.RevisionID, err = memory.RevisionIDForRecord(record.CanonicalKey, record, "insert", "")
	if err != nil {
		return CandidateResult{}, err
	}
	record.BlockID = memory.BlockIDForCanonicalKey(record.CanonicalKey)
	record.BlockMembership = nil
	if err := engine.BindRootRecord(ctx, &record, route.BlockID); err != nil {
		return CandidateResult{}, err
	}
	receipt, err := engine.PutRoot(ctx, record)
	if err != nil {
		return CandidateResult{}, err
	}
	linked := rawWitnessProvenanceReceipts(record.BlockID, rawWitnessIDs,
		replayed || receipt.Replayed)
	return CandidateResult{Saved: true, RecordID: record.BlockID, OperationID: request.OperationID,
		Replayed: replayed || receipt.Replayed, Route: route, Receipt: &receipt,
		ProvenanceEdges: linked}, nil
}
