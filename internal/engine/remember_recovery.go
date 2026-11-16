package engine

import (
	"context"
	"errors"
	"fmt"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// recoverCandidateOperation closes the gap between a staged or committed
// canonical record and its operation receipt. The immutable request fingerprint
// and authenticated caller binding must match before projection is retried.
func (engine *Engine) recoverCandidateOperation(ctx context.Context,
	request CandidateRequest) (CandidateResult, bool, error) {
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return CandidateResult{}, false, err
	}
	var matched *store.ProjectionRow
	for _, row := range rows {
		if row.Record.Metadata["operation_id"] != request.OperationID ||
			row.Backend != engine.recordNativeStore(row.Record) {
			continue
		}
		if matched != nil {
			return CandidateResult{}, false, errors.New("candidate operation has multiple canonical records")
		}
		copy := row
		matched = &copy
	}
	if matched == nil {
		return CandidateResult{}, false, nil
	}
	record := matched.Record
	if record.Metadata["operation_caller_role"] != request.operationCallerRole ||
		fmt.Sprint(record.Metadata["operation_protocol_version"]) !=
			fmt.Sprint(request.operationProtocolVersion) ||
		record.Metadata["operation_request_fingerprint"] != request.operationFingerprint ||
		record.Content != request.Content || record.SessionID != request.SessionID ||
		record.ProjectID != request.ProjectID || record.BlockMembership == nil ||
		record.BlockMembership.PrimaryBlockID != request.RequestedBlockID ||
		record.Metadata["write_mode"] != defaultValue(request.WriteMode, "standard") ||
		(request.proposalCanonicalKey != "" && record.CanonicalKey != request.proposalCanonicalKey) {
		return CandidateResult{}, false, errors.New("creation operation receipt conflicts with canonical record")
	}
	if matched.Status != "pending" && matched.Status != "committed" {
		return CandidateResult{}, false, errors.New("candidate operation canonical write is not recoverable")
	}
	receipt := store.WriteReceipt{RecordID: record.BlockID,
		CommitRevision: matched.Revision, Replayed: true}
	if matched.Status == "pending" {
		receipt, err = engine.writer.CompleteProjection(ctx, matched.Revision, engine.projector)
		if err != nil {
			return CandidateResult{}, false, err
		}
	}
	route := memory.RouteDecision{Accepted: true,
		Reason:    fmt.Sprint(record.Metadata["route_decision_reason"]),
		BlockID:   record.BlockMembership.PrimaryBlockID,
		Retention: record.Retention, DeliveryPolicy: record.DeliveryPolicy,
		Temperature: record.Temperature, Authority: record.Authority,
		Scope: record.Scope, TrustLevel: record.TrustLevel,
		VerificationStatus:  record.VerificationStatus,
		VerificationMethods: record.VerificationMethods,
		PromotionDecision:   record.PromotionDecision, Status: record.Status,
		Tags: record.Tags, CatalogRevisionID: record.BlockMembership.CatalogRevisionID}
	return CandidateResult{Saved: true, RecordID: record.BlockID,
		OperationID: request.OperationID, Replayed: true, Route: route,
		Receipt: &receipt}, true, nil
}
