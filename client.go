// Package ummem exposes the Go memory client for applications embedding UM-MEM.
// Host routing, proof validity and task completion remain the host's authority;
// records and recalled context are advisory.
package ummem

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/catalog"
	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/frontier-ai-next/Um-mem/internal/mcpserver"
	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
)

// Config selects one workspace, profile and native backend for the client.
type Config = engine.Config

// Client owns the selected backend and its canonical store. Close it when done.
// Its exported methods are the same operations used by the Go CLI and MCP server.
type Client struct{ *engine.Engine }

func Open(config Config) (*Client, error) {
	selected, err := engine.Open(config)
	if err != nil {
		return nil, err
	}
	return &Client{Engine: selected}, nil
}

type CandidateRequest = engine.CandidateRequest
type CandidateResult = engine.CandidateResult
type AmendRequest = engine.AmendRequest
type CorrectRequest = engine.CorrectRequest
type ForgetRequest = engine.ForgetRequest
type DeprecateRequest = engine.DeprecateRequest
type RollbackRequest = engine.RollbackRequest
type CreateBlockRequest = engine.CreateBlockRequest
type AmendBlockRequest = engine.AmendBlockRequest
type MoveBlockRequest = engine.MoveBlockRequest
type BlockTransitionRequest = engine.BlockTransitionRequest
type DeleteBlockRequest = engine.DeleteBlockRequest
type SplitBlockRequest = engine.SplitBlockRequest
type MergeBlocksRequest = engine.MergeBlocksRequest
type AddEdgeRequest = engine.AddEdgeRequest
type ActiveMaskRequest = engine.ActiveMaskRequest
type ListRecordsRequest = engine.ListRecordsRequest
type RecentRequest = engine.RecentRequest
type ConsolidateObservationsRequest = engine.ConsolidateObservationsRequest
type CommitWorkingCheckpointRequest = engine.CommitWorkingCheckpointRequest
type VerificationSubjectRequest = engine.VerificationSubjectRequest
type PromotionReviewRequest = engine.PromotionReviewRequest
type PromoteRequest = engine.PromoteRequest
type CompensatePromotionRequest = engine.CompensatePromotionRequest
type CompensationReceipt = engine.CompensationReceipt
type ProcedureRequest = engine.ProcedureRequest
type RetractWorkingRequest = engine.RetractWorkingRequest
type RepairProtectedBlockParentRequest = engine.RepairProtectedBlockParentRequest
type SessionBlockRequest = engine.SessionBlockRequest
type ConvertBlockToIndexRequest = engine.ConvertBlockToIndexRequest
type MaskAmendment = catalog.MaskAmendment
type RecallPlan = memory.RecallPlan
type Record = memory.MemoryRecord
type EraseRequest = memory.MemoryEraseRequest
type DependencyOptions = store.DependencyOptions
type DependencyValidation = memory.DependencyValidation
type NativePartitionIdentity = memory.NativePartitionIdentity
type NativePartitionMetrics = memory.NativePartitionMetrics
type PinnedRecordRef = engine.PinnedRecordRef
type WorkingRecordSnapshot = engine.WorkingRecordSnapshot
type WorkingSetRead = engine.WorkingSetRead
type ContextualUtilityRequest = engine.ContextualUtilityRequest
type ContextualUtilitySummary = memory.ContextualUtilitySummary
type TransferEnvelope = memory.TransferEnvelope
type ProcedureReuseDecision = memory.ProcedureReuseDecision
type RecallArgs = mcpserver.RecallArgs
type ExplainRecallRequest = mcpserver.ExplainRecallRequest
type DeepReadRequest = mcpserver.DeepReadRequest
type ActivationLease = memory.ActivationLease

func NewRecallPlan(query string) RecallPlan { return memory.NewRecallPlan(query) }

func (client *Client) QualifyProcedureReuse(memoryID, sourceTaskSignature,
	requestedTaskSignature string, envelope *TransferEnvelope) (ProcedureReuseDecision, error) {
	return memory.QualifyProcedureReuse(memoryID, sourceTaskSignature,
		requestedTaskSignature, envelope)
}

// BuildContext packs scoped records into bounded, advisory context. The host
// must supply its authenticated scope chain and run ID.
func (client *Client) BuildContext(ctx context.Context, args RecallArgs,
	scopeChain []string, runID string, render bool) (map[string]any, error) {
	workspaceID, _ := client.Scope()
	return mcpserver.BuildContextPayload(ctx, client.Engine, args,
		workspaceID, client.BackendID(), scopeChain, runID, render)
}

// BuildActivationLease returns bounded advisory evidence and an out-of-prompt
// selection trace for the host's authenticated scope and run.
func (client *Client) BuildActivationLease(ctx context.Context, args RecallArgs,
	scopeChain []string, runID string) (ActivationLease, error) {
	workspaceID, _ := client.Scope()
	return mcpserver.BuildActivationLeasePayload(ctx, client.Engine, args,
		workspaceID, client.BackendID(), scopeChain, runID)
}

// ExplainRecall keeps the selection trace outside the rendered prompt.
func (client *Client) ExplainRecall(ctx context.Context, request ExplainRecallRequest,
	scopeChain []string, runID string) (memory.RecallExplanation, error) {
	workspaceID, _ := client.Scope()
	return mcpserver.ExplainRecallPayload(ctx, client.Engine, request,
		workspaceID, client.BackendID(), scopeChain, runID)
}

// DeepRead remains bound to one logical block and returns an advisory lease.
func (client *Client) DeepRead(ctx context.Context, request DeepReadRequest) (memory.DeepReadResult, error) {
	workspaceID, _ := client.Scope()
	return mcpserver.DeepReadPayload(ctx, client.Engine, request, workspaceID, client.BackendID())
}
