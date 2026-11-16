package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/ledger"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/receipts"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type PromoteRequest struct {
	PromotionReviewRequest
	Reason      string `json:"reason"`
	OperationID string `json:"operation_id"`
}

func publicationArguments(request PromoteRequest, workspaceRoot string) map[string]any {
	var targetScope any
	if request.TargetScope != "" {
		targetScope = request.TargetScope
	}
	reports := request.Reports
	if reports == nil {
		reports = []ledger.VerificationReport{}
	}
	refs := request.SourceEvidenceRefs
	if refs == nil {
		refs = []memory.EvidenceRef{}
	}
	changed := request.ChangedFiles
	if changed == nil {
		changed = []string{}
	}
	authority := request.Authority
	if authority == "" {
		authority = "advisory"
	}
	return map[string]any{"tool": "promote", "memory_id": request.MemoryID,
		"reason": request.Reason, "evidence_refs": reports,
		"source_evidence_refs": refs, "authority": authority,
		"target_block_id": request.TargetBlockID, "target_scope": targetScope,
		"target_scope_id":             request.TargetScopeID,
		"expected_source_revision_id": request.ExpectedSourceRevision,
		"workspace_root":              workspaceRoot, "changed_files": changed,
		"allow_workspace_hash_mismatch_if_diff_matches": request.AllowWorkspaceHashMismatch}
}

func (engine *Engine) bindPublicationTarget(request *PromoteRequest,
	source memory.MemoryRecord) (string, string, error) {
	if request.TargetBlockID == "" && request.TargetScope == "" {
		if request.TargetScopeID != "" {
			return "", "", errors.New("target_scope_id requires a publication target scope")
		}
		return source.Scope, subjectRecordScopeID(source), nil
	}
	scope := request.TargetScope
	if request.TargetBlockID != "" {
		if err := engine.RefreshCatalog(); err != nil {
			return "", "", err
		}
		block, ok := engine.current.Load().byID[request.TargetBlockID]
		if !ok || block.IndexOnly || len(block.ChildIDs) != 0 ||
			(scope != "" && scope != block.Scope) {
			return "", "", errors.New("publication target leaf or scope is invalid")
		}
		scope = block.Scope
	}
	bound := ""
	switch scope {
	case "workspace":
		bound = engine.config.WorkspaceID
	case "project":
		bound = request.Lineage.ProjectID
	case "user_global":
		bound = request.Lineage.UserID
	case "session":
		bound = request.Lineage.SessionID
	case "task":
		bound = request.Lineage.TaskID
	case "subtask":
		bound = request.Lineage.SubtaskID
	default:
		return "", "", errors.New("publication target scope is unsupported")
	}
	if !exactSubjectIdentity(bound) ||
		(request.TargetScopeID != "" && request.TargetScopeID != bound) {
		return "", "", errors.New("publication target is outside caller scope lineage")
	}
	return scope, bound, nil
}

func publicationTargetMatches(receipt memory.PublicationCommitReceipt,
	request PromoteRequest, source memory.MemoryRecord, scope, scopeID string) bool {
	targetBlockID := request.TargetBlockID
	if targetBlockID == "" && source.BlockMembership != nil {
		targetBlockID = source.BlockMembership.PrimaryBlockID
	}
	authority := request.Authority
	if authority == "" {
		authority = "advisory"
	}
	return receipt.TargetBlockID == targetBlockID && receipt.TargetScope == scope &&
		receipt.TargetScopeID == scopeID && receipt.Authority == authority &&
		receipt.MaterializedCopy == (request.TargetBlockID != "")
}

func publicationResponse(receipt memory.PublicationCommitReceipt, backend string, replayed bool) map[string]any {
	response := receipt.Response(backend)
	if replayed {
		response["replayed"] = true
	}
	return response
}

func (engine *Engine) completePublicationOperation(ctx context.Context,
	identity receipts.Identity, request PromoteRequest, arguments map[string]any,
	receipt memory.PublicationCommitReceipt, replayed bool) (map[string]any, error) {
	response := receipt.Response(engine.config.BackendID)
	wrapper := map[string]any{"receipt_kind": "mutation_response", "mutation_tool": "promote",
		"response": response}
	if err := receipts.Complete(ctx, engine.config.RepoRoot, identity,
		request.OperationID, arguments, wrapper, "committed"); err != nil {
		return nil, err
	}
	return publicationResponse(receipt, engine.config.BackendID, replayed), nil
}

// Promote publishes an independently verified candidate as an accepted
// revision, a materialized copy, or an approved correction. A durable
// operation ID binds retries to the exact request and canonical receipt.
func (engine *Engine) Promote(ctx context.Context,
	request PromoteRequest) (map[string]any, error) {
	return engine.PromoteFor(ctx, request, "memory_client", 2)
}

func (engine *Engine) PromoteFor(ctx context.Context, request PromoteRequest,
	callerRole string, protocolVersion int) (map[string]any, error) {
	if request.Reason == "" || strings.TrimSpace(request.Reason) == "" ||
		request.OperationID == "" || request.OperationID != strings.TrimSpace(request.OperationID) ||
		len(request.OperationID) > 256 || callerRole == "" || protocolVersion < 1 {
		return nil, errors.New("promotion requires a reason, bounded operation ID and caller identity")
	}
	if !exactSubjectIdentity(request.MemoryID) || !exactSubjectIdentity(request.ExpectedSourceRevision) {
		return nil, errors.New("promotion requires exact candidate and revision identities")
	}
	lock, err := engine.acquireEraseWorkspaceLock(ctx)
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	source, err := engine.ReadStored(ctx, request.MemoryID)
	if err != nil {
		return nil, err
	}
	if source == nil || source.RevisionID != request.ExpectedSourceRevision {
		return nil, errors.New("promotion source revision is unavailable")
	}
	if err := engine.assertChainNotErased(ctx, source.CanonicalKey); err != nil {
		return nil, err
	}
	if err := requirePromotionSourceLineage(*source, request.Lineage); err != nil {
		return nil, err
	}
	targetScope, targetScopeID, err := engine.bindPublicationTarget(&request, *source)
	if err != nil {
		return nil, err
	}
	workspaceRoot := request.WorkspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = engine.config.RepoRoot
	}
	workspaceRoot, err = filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, err
	}
	workspaceRoot, err = filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return nil, err
	}
	request.WorkspaceRoot = workspaceRoot
	arguments := publicationArguments(request, workspaceRoot)
	requestHash, err := receipts.Fingerprint(arguments)
	if err != nil {
		return nil, err
	}
	publicationHash, err := memory.PythonHashValue([]string{engine.config.WorkspaceID,
		engine.config.ProfileID, request.OperationID})
	if err != nil {
		return nil, err
	}
	publicationID := "publication_" + publicationHash
	identity := receipts.Identity{WorkspaceID: engine.config.WorkspaceID,
		ProfileID: engine.config.ProfileID, Backend: engine.config.BackendID,
		CallerRole: callerRole, ProtocolVersion: protocolVersion}
	claimed, err := receipts.Claim(ctx, engine.config.RepoRoot, identity, request.OperationID, arguments)
	if err != nil {
		return nil, err
	}
	if claimed.Status == "committed" {
		receipt, err := engine.RecoverPublication(ctx, publicationID, requestHash,
			request.MemoryID, request.ExpectedSourceRevision)
		if err != nil || receipt == nil ||
			!publicationTargetMatches(*receipt, request, *source, targetScope, targetScopeID) {
			return nil, errors.New("committed promotion has no matching canonical publication")
		}
		return publicationResponse(*receipt, engine.config.BackendID, true), nil
	}
	committed, err := engine.RecoverPublication(ctx, publicationID, requestHash,
		request.MemoryID, request.ExpectedSourceRevision)
	if err != nil {
		return nil, err
	}
	if committed != nil {
		if !publicationTargetMatches(*committed, request, *source, targetScope, targetScopeID) {
			return nil, errors.New("canonical publication target is outside caller lineage")
		}
		return engine.completePublicationOperation(ctx, identity, request, arguments, *committed, true)
	}
	review, err := engine.ValidatePromotion(ctx, request.PromotionReviewRequest)
	if err != nil {
		return nil, err
	}
	if review.Source.BlockMembership == nil {
		return nil, errors.New("publication source has no exact logical leaf")
	}
	pending, err := engine.index.PendingPublication(ctx, publicationID)
	if err != nil {
		return nil, err
	}
	if pending != nil {
		receipt := pending.Receipt
		if receipt.RequestHash != requestHash || receipt.SourceID != request.MemoryID ||
			receipt.SourceRevisionID != request.ExpectedSourceRevision ||
			receipt.TargetBlockID != review.Subject.TargetBlockID ||
			receipt.TargetScope != review.Subject.Scope ||
			receipt.TargetScopeID != review.Subject.ScopeID ||
			receipt.Authority != review.Subject.Authority ||
			receipt.MaterializedCopy != (request.TargetBlockID != "") {
			return nil, errors.New("pending publication conflicts with exact request")
		}
		if _, err := engine.writer.CompleteProjection(ctx, pending.CommitRevision, engine.projector); err != nil {
			return nil, err
		}
		committed, err := engine.RecoverPublication(ctx, publicationID, requestHash,
			request.MemoryID, request.ExpectedSourceRevision)
		if err != nil || committed == nil {
			return nil, errors.New("pending publication did not produce a committed receipt")
		}
		return engine.completePublicationOperation(ctx, identity, request, arguments, *committed, true)
	}
	if request.TargetBlockID != "" {
		return engine.publishMaterializedCopy(ctx, request, identity, arguments,
			review, publicationID, requestHash)
	}
	if sourceTarget, _ := review.Source.Metadata["correction_target_id"].(string); sourceTarget != "" {
		return engine.publishApprovedCorrection(ctx, request, identity, arguments,
			review, publicationID, requestHash, claimed.CreatedAt, sourceTarget)
	}
	if review.Subject.Scope != review.Source.Scope ||
		review.Subject.ScopeID != subjectRecordScopeID(review.Source) {
		return nil, errors.New("cross-leaf or cross-scope publication requires materialized copy")
	}
	claimedTime, err := time.Parse(time.RFC3339Nano, claimed.CreatedAt)
	if err != nil {
		return nil, errors.New("promotion operation claim time is invalid")
	}
	stamp := claimedTime.UTC().Format("2006-01-02T15:04:05.000000Z")
	accepted := review.Source
	accepted.Metadata = make(map[string]any, len(review.Source.Metadata)+5)
	for key, value := range review.Source.Metadata {
		accepted.Metadata[key] = value
	}
	accepted.Metadata["promotion_operation_id"] = request.OperationID
	accepted.Metadata["promotion_source_id"] = review.Source.BlockID
	accepted.Metadata["promotion_source_revision_id"] = review.Source.RevisionID
	accepted.Metadata["candidate_semantic_verification"] = review.SemanticReceipt
	for key, value := range review.ProcedureGateReceipt {
		accepted.Metadata[key] = value
	}
	accepted.Status = "accepted"
	accepted.Authority = review.Subject.Authority
	accepted.TrustLevel = "workspace_verified"
	accepted.VerificationStatus = "verified"
	accepted.VerificationMethods = []string{"verification_report"}
	accepted.PromotionDecision = "accepted"
	accepted.EvidenceRefs, err = mergeEvidenceRefs(review.Source.EvidenceRefs, review.PromotionRefs)
	if err != nil {
		return nil, err
	}
	accepted.RecordedAt, accepted.ValidFrom, accepted.ValidTo = stamp, stamp, ""
	accepted.UpdatedAt = stamp
	accepted.RevisionOf = review.Source.BlockID
	accepted.SupersedesIDs = []string{review.Source.BlockID}
	selectedID := review.Source.BlockMembership.PrimaryBlockID
	accepted.BlockMembership = &memory.RecordBlockMembership{PrimaryBlockID: selectedID,
		SecondaryBlockIDs: append([]string{}, review.Source.BlockMembership.SecondaryBlockIDs...)}
	accepted.RevisionID, err = memory.RevisionIDForRecord(review.Source.CanonicalKey,
		accepted, "promote", review.Source.RevisionID)
	if err != nil {
		return nil, err
	}
	accepted.BlockID = memory.BlockIDForRevisionID(accepted.RevisionID)
	accepted.BlockMembership = nil
	if err := engine.BindSuccessorRecord(ctx, &accepted, selectedID); err != nil {
		return nil, err
	}
	receipt := memory.PublicationCommitReceipt{ReceiptID: publicationID,
		RequestHash: requestHash, WorkspaceID: engine.config.WorkspaceID,
		ProfileID: engine.config.ProfileID, SourceID: review.Source.BlockID,
		SourceRevisionID: review.Source.RevisionID, RecordID: accepted.BlockID,
		RevisionID: accepted.RevisionID, TargetBlockID: selectedID,
		TargetScope: review.Subject.Scope, TargetScopeID: review.Subject.ScopeID,
		Authority: review.Subject.Authority, MaterializedCopy: false}
	if err := engine.validateBoundRecord(ctx, accepted); err != nil {
		return nil, err
	}
	staged, err := engine.writer.PreparePublicationSuccessorWrite(ctx, accepted, "promote", receipt)
	if err != nil {
		return nil, err
	}
	if _, err := engine.writer.CompleteProjection(ctx, staged.CommitRevision, engine.projector); err != nil {
		return nil, err
	}
	return engine.completePublicationOperation(ctx, identity, request, arguments, receipt, false)
}

func (engine *Engine) publishMaterializedCopy(ctx context.Context, request PromoteRequest,
	identity receipts.Identity, arguments map[string]any, review PromotionReview,
	publicationID, requestHash string) (map[string]any, error) {
	if review.Subject.Authority != "advisory" {
		return nil, errors.New("publication into another leaf remains advisory")
	}
	if err := engine.RefreshCatalog(); err != nil {
		return nil, err
	}
	block, exists := engine.current.Load().byID[review.Subject.TargetBlockID]
	if !exists || block.IndexOnly || len(block.ChildIDs) != 0 ||
		block.Scope != review.Subject.Scope {
		return nil, errors.New("publication target is no longer a storable leaf")
	}
	material := review.Source.CanonicalKey + "|" + review.Source.RevisionID + "|" +
		block.BlockID + "|" + review.Subject.Scope + "|" + review.Subject.ScopeID
	digest := sha256.Sum256([]byte(material))
	canonicalKey := engine.config.WorkspaceID + ":" + engine.config.ProfileID +
		":publication:" + hex.EncodeToString(digest[:])[:24]
	copyID := memory.BlockIDForCanonicalKey(canonicalKey)
	existing, err := engine.ReadStored(ctx, copyID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.CanonicalKey != canonicalKey || existing.WorkspaceID != engine.config.WorkspaceID ||
			existing.ProfileID != engine.config.ProfileID || existing.BlockMembership == nil ||
			existing.BlockMembership.PrimaryBlockID != block.BlockID ||
			existing.Metadata["promotion_source_id"] != review.Source.BlockID ||
			existing.Metadata["promotion_source_revision_id"] != review.Source.RevisionID ||
			existing.Content != review.Source.Content || existing.Status != "accepted" ||
			existing.VerificationStatus != "verified" ||
			existing.PromotionDecision != "accepted" || existing.Authority != "advisory" ||
			existing.Scope != review.Subject.Scope ||
			subjectRecordScopeID(*existing) != review.Subject.ScopeID {
			return nil, errors.New("verified copy conflicts with an existing canonical record")
		}
		receipt := memory.PublicationCommitReceipt{ReceiptID: publicationID,
			RequestHash: requestHash, WorkspaceID: engine.config.WorkspaceID,
			ProfileID: engine.config.ProfileID, SourceID: review.Source.BlockID,
			SourceRevisionID: review.Source.RevisionID, RecordID: existing.BlockID,
			RevisionID: existing.RevisionID, TargetBlockID: block.BlockID,
			TargetScope: review.Subject.Scope, TargetScopeID: review.Subject.ScopeID,
			Authority: "advisory", MaterializedCopy: true}
		if err := engine.writer.AttachPublicationCopyReceipt(ctx, receipt); err != nil {
			return nil, err
		}
		return engine.completePublicationOperation(ctx, identity, request, arguments, receipt, true)
	}
	accepted := review.Source
	accepted.CanonicalKey = canonicalKey
	accepted.BlockID = copyID
	accepted.RevisionOf = ""
	accepted.SupersedesIDs = []string{}
	accepted.BlockMembership = &memory.RecordBlockMembership{PrimaryBlockID: block.BlockID,
		SecondaryBlockIDs: []string{}}
	accepted.Status = "accepted"
	accepted.Retention = block.Retention
	accepted.DeliveryPolicy = block.DeliveryPolicy
	accepted.Temperature = block.Temperature
	accepted.Authority = "advisory"
	accepted.TrustLevel = "workspace_verified"
	accepted.Scope = block.Scope
	accepted.VerificationStatus = "verified"
	accepted.VerificationMethods = []string{"verification_report"}
	accepted.PromotionDecision = "accepted"
	accepted.ProjectID, accepted.UserID = review.Source.ProjectID, review.Source.UserID
	accepted.SessionID, accepted.TaskID = review.Source.SessionID, review.Source.TaskID
	accepted.SubtaskID = review.Source.SubtaskID
	setPublicationScope(&accepted, review.Subject.Scope, review.Subject.ScopeID)
	accepted.EvidenceRefs, err = mergeEvidenceRefs(review.Source.EvidenceRefs, review.PromotionRefs)
	if err != nil {
		return nil, err
	}
	accepted.Metadata = make(map[string]any, len(review.Source.Metadata)+6)
	for key, value := range review.Source.Metadata {
		accepted.Metadata[key] = value
	}
	accepted.Metadata["promotion_source_id"] = review.Source.BlockID
	accepted.Metadata["promotion_source_revision_id"] = review.Source.RevisionID
	accepted.Metadata["promoted_to_block_id"] = block.BlockID
	accepted.Metadata["status_reason"] = request.Reason
	accepted.Metadata["candidate_semantic_verification"] = review.SemanticReceipt
	for key, value := range review.ProcedureGateReceipt {
		accepted.Metadata[key] = value
	}
	accepted.RevisionID, err = memory.RevisionIDForRecord(canonicalKey, accepted, "insert", "")
	if err != nil {
		return nil, err
	}
	accepted.BlockMembership = nil
	if err := engine.BindRootRecord(ctx, &accepted, block.BlockID); err != nil {
		return nil, err
	}
	receipt := memory.PublicationCommitReceipt{ReceiptID: publicationID,
		RequestHash: requestHash, WorkspaceID: engine.config.WorkspaceID,
		ProfileID: engine.config.ProfileID, SourceID: review.Source.BlockID,
		SourceRevisionID: review.Source.RevisionID, RecordID: accepted.BlockID,
		RevisionID: accepted.RevisionID, TargetBlockID: block.BlockID,
		TargetScope: review.Subject.Scope, TargetScopeID: review.Subject.ScopeID,
		Authority: "advisory", MaterializedCopy: true}
	if err := engine.validateBoundRecord(ctx, accepted); err != nil {
		return nil, err
	}
	staged, err := engine.writer.PreparePublicationRootWrite(ctx, accepted, receipt)
	if err != nil {
		return nil, err
	}
	if _, err := engine.writer.CompleteProjection(ctx, staged.CommitRevision, engine.projector); err != nil {
		return nil, err
	}
	return engine.completePublicationOperation(ctx, identity, request, arguments, receipt, false)
}

func setPublicationScope(record *memory.MemoryRecord, scope, scopeID string) {
	switch scope {
	case "project":
		record.ProjectID = scopeID
	case "user_global":
		record.UserID = scopeID
	case "session":
		record.SessionID = scopeID
	case "task":
		record.TaskID = scopeID
	case "subtask":
		record.SubtaskID = scopeID
	}
}

func (engine *Engine) publishApprovedCorrection(ctx context.Context, request PromoteRequest,
	identity receipts.Identity, arguments map[string]any, review PromotionReview,
	publicationID, requestHash, claimedAt, targetID string) (map[string]any, error) {
	target, err := engine.ReadStored(ctx, targetID)
	if err != nil {
		return nil, err
	}
	if target == nil || target.BlockMembership == nil || review.Source.BlockMembership == nil ||
		target.RevisionID != review.Source.Metadata["correction_target_revision_id"] ||
		target.BlockMembership.PrimaryBlockID != review.Source.BlockMembership.PrimaryBlockID ||
		review.Subject.TargetBlockID != target.BlockMembership.PrimaryBlockID ||
		review.Subject.Scope != target.Scope ||
		review.Subject.ScopeID != subjectRecordScopeID(*target) {
		return nil, errors.New("approved correction candidate targets another revision, scope or leaf")
	}
	current, err := engine.index.IsCurrentRecord(ctx, *target)
	if err != nil {
		return nil, err
	}
	if !current {
		return nil, errors.New("correction target changed before host approval")
	}
	claimedTime, err := time.Parse(time.RFC3339Nano, claimedAt)
	if err != nil {
		return nil, errors.New("promotion operation claim time is invalid")
	}
	stamp := claimedTime.UTC().Format("2006-01-02T15:04:05.000000Z")
	accepted := *target
	accepted.Title = review.Source.Title
	accepted.Content = review.Source.Content
	accepted.Tags = append([]string{}, review.Source.Tags...)
	accepted.ChildClaims = append([]json.RawMessage{}, review.Source.ChildClaims...)
	accepted.Metadata = make(map[string]any, len(target.Metadata)+len(review.Source.Metadata)+8)
	for key, value := range target.Metadata {
		accepted.Metadata[key] = value
	}
	for key, value := range review.Source.Metadata {
		accepted.Metadata[key] = value
	}
	delete(accepted.Metadata, "pending_host_approval")
	accepted.Metadata["approved_correction_candidate_id"] = review.Source.BlockID
	accepted.Metadata["status_reason"] = request.Reason
	accepted.Metadata["promotion_operation_id"] = request.OperationID
	accepted.Metadata["promotion_source_id"] = review.Source.BlockID
	accepted.Metadata["promotion_source_revision_id"] = review.Source.RevisionID
	accepted.Metadata["candidate_semantic_verification"] = review.SemanticReceipt
	for key, value := range review.ProcedureGateReceipt {
		accepted.Metadata[key] = value
	}
	accepted.Status = "accepted"
	accepted.Authority = review.Subject.Authority
	accepted.TrustLevel = "workspace_verified"
	accepted.VerificationStatus = "verified"
	accepted.VerificationMethods = []string{"verification_report"}
	accepted.PromotionDecision = "accepted"
	accepted.Scope = review.Subject.Scope
	accepted.ProjectID, accepted.UserID = review.Source.ProjectID, review.Source.UserID
	accepted.SessionID, accepted.TaskID = review.Source.SessionID, review.Source.TaskID
	accepted.SubtaskID = review.Source.SubtaskID
	setPublicationScope(&accepted, review.Subject.Scope, review.Subject.ScopeID)
	refs, err := mergeEvidenceRefs(target.EvidenceRefs, review.Source.EvidenceRefs)
	if err != nil {
		return nil, err
	}
	accepted.EvidenceRefs, err = mergeEvidenceRefs(refs, review.PromotionRefs)
	if err != nil {
		return nil, err
	}
	accepted.DuplicateClusterID, err = memory.DuplicateClusterID(accepted)
	if err != nil {
		return nil, err
	}
	accepted.RecordedAt, accepted.ValidFrom, accepted.ValidTo = stamp, stamp, ""
	accepted.UpdatedAt = stamp
	accepted.RevisionOf = target.BlockID
	accepted.SupersedesIDs = []string{target.BlockID}
	selectedID := target.BlockMembership.PrimaryBlockID
	accepted.BlockMembership = &memory.RecordBlockMembership{PrimaryBlockID: selectedID,
		SecondaryBlockIDs: append([]string{}, target.BlockMembership.SecondaryBlockIDs...)}
	accepted.RevisionID, err = memory.RevisionIDForRecord(target.CanonicalKey,
		accepted, "correction", target.RevisionID)
	if err != nil {
		return nil, err
	}
	accepted.BlockID = memory.BlockIDForRevisionID(accepted.RevisionID)
	accepted.BlockMembership = nil
	if err := engine.BindSuccessorRecord(ctx, &accepted, selectedID); err != nil {
		return nil, err
	}
	receipt := memory.PublicationCommitReceipt{ReceiptID: publicationID,
		RequestHash: requestHash, WorkspaceID: engine.config.WorkspaceID,
		ProfileID: engine.config.ProfileID, SourceID: review.Source.BlockID,
		SourceRevisionID: review.Source.RevisionID, RecordID: accepted.BlockID,
		RevisionID: accepted.RevisionID, TargetBlockID: selectedID,
		TargetScope: review.Subject.Scope, TargetScopeID: review.Subject.ScopeID,
		Authority: review.Subject.Authority, MaterializedCopy: false}
	if err := engine.validateBoundRecord(ctx, accepted); err != nil {
		return nil, err
	}
	staged, err := engine.writer.PreparePublicationSuccessorWrite(ctx, accepted, "correction", receipt)
	if err != nil {
		return nil, err
	}
	if _, err := engine.writer.CompleteProjection(ctx, staged.CommitRevision, engine.projector); err != nil {
		return nil, err
	}
	return engine.completePublicationOperation(ctx, identity, request, arguments, receipt, false)
}
