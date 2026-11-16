package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/engine"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RememberArgs covers agent-authored add, repair, and correction writes.
type RememberArgs struct {
	Content          string               `json:"content"`
	RequestedBlockID string               `json:"requested_block_id"`
	Operation        string               `json:"operation,omitempty"`
	MemoryID         string               `json:"memory_id,omitempty"`
	ExpectedRevision string               `json:"expected_revision,omitempty"`
	Reason           string               `json:"reason,omitempty"`
	RepairOf         string               `json:"repair_of,omitempty"`
	WriteMode        string               `json:"write_mode,omitempty"`
	OperationID      string               `json:"operation_id,omitempty"`
	Title            string               `json:"title,omitempty"`
	Kind             string               `json:"kind,omitempty"`
	Tags             []string             `json:"tags,omitempty"`
	Metadata         map[string]any       `json:"metadata,omitempty"`
	SourceSegments   []map[string]any     `json:"source_segments,omitempty"`
	EvidenceRefs     []memory.EvidenceRef `json:"evidence_refs,omitempty"`
	EvidenceKind     string               `json:"evidence_kind,omitempty"`
	SourceID         string               `json:"source_id,omitempty"`
	Sensitivity      string               `json:"sensitivity,omitempty"`
	ContentMode      string               `json:"content_mode,omitempty"`
	OperationalRisk  string               `json:"operational_risk,omitempty"`
	RunID            string               `json:"run_id,omitempty"`
	StageID          string               `json:"stage_id,omitempty"`
	ArtifactID       string               `json:"artifact_id,omitempty"`
	ArtifactRevision string               `json:"artifact_revision,omitempty"`
	ValidFrom        string               `json:"valid_from,omitempty"`
	ValidTo          string               `json:"valid_to,omitempty"`
	RepositoryID     string               `json:"repository_id,omitempty"`
	Checkout         string               `json:"checkout,omitempty"`
	Worktree         string               `json:"worktree,omitempty"`
	Branch           string               `json:"branch,omitempty"`
	Revision         string               `json:"revision,omitempty"`
	DirtyState       string               `json:"dirty_state,omitempty"`
	ProjectID        string               `json:"project_id,omitempty"`
	SessionID        string               `json:"session_id,omitempty"`
	TaskID           string               `json:"task_id,omitempty"`
	SubtaskID        string               `json:"subtask_id,omitempty"`
	ParentSessionID  string               `json:"parent_session_id,omitempty"`
	UserID           string               `json:"user_id,omitempty"`
}

func newOperationID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(random[:]), nil
}

func correctionPayload(ctx context.Context, selected *engine.Engine, args RememberArgs,
	workspaceID, backendID, callerRole string, protocolVersion int) (map[string]any, error) {
	if args.MemoryID == "" || args.RepairOf != "" || args.WriteMode == "lossless_atomic" ||
		args.SourceSegments != nil {
		return nil, errors.New("correct requires memory_id and forbids repair or lossless source fields")
	}
	if args.OperationID == "" {
		var err error
		args.OperationID, err = newOperationID()
		if err != nil {
			return nil, err
		}
	}
	result, err := selected.CorrectFor(ctx, engine.CorrectRequest{
		MemoryID: args.MemoryID, Content: args.Content, Title: args.Title,
		Reason: args.Reason, ExpectedRevision: args.ExpectedRevision,
		RequestedBlockID: args.RequestedBlockID, Metadata: args.Metadata,
		EvidenceRefs: args.EvidenceRefs, OperationID: args.OperationID,
		SessionID: args.SessionID, ProjectID: args.ProjectID, TaskID: args.TaskID,
		SubtaskID: args.SubtaskID, UserID: args.UserID,
		ParentSessionID: args.ParentSessionID,
	}, callerRole, protocolVersion)
	if err != nil {
		return nil, err
	}
	result["operation"] = "correct"
	result["operation_id"] = args.OperationID
	result["requested_block_id"] = args.RequestedBlockID
	result["route_rejected"] = false
	result["backend"] = backendID
	result["workspace_id"] = workspaceID
	return result, nil
}

func RememberPayload(ctx context.Context, selected *engine.Engine, args RememberArgs,
	workspaceID, backendID, callerRole string, protocolVersion int) (map[string]any, error) {
	if strings.TrimSpace(args.RequestedBlockID) == "" {
		return nil, errors.New("remember requires requested_block_id")
	}
	if args.RequestedBlockID != strings.TrimSpace(args.RequestedBlockID) {
		return nil, errors.New("requested_block_id must be exact")
	}
	operation := args.Operation
	if operation == "" {
		operation = "add"
	}
	if operation != "add" && operation != "repair" && operation != "correct" {
		return nil, errors.New("remember operation must be add, correct, or repair")
	}
	if operation == "correct" {
		return correctionPayload(ctx, selected, args, workspaceID, backendID, callerRole, protocolVersion)
	}
	if args.MemoryID != "" {
		return nil, errors.New("add and repair operations cannot supply memory_id")
	}
	if operation == "add" && args.RepairOf != "" {
		return nil, errors.New("add operation forbids repair_of")
	}
	if operation == "repair" {
		if args.RepairOf == "" || args.RepairOf != strings.TrimSpace(args.RepairOf) {
			return nil, errors.New("repair operation requires repair_of")
		}
		matched := false
		for _, ref := range args.EvidenceRefs {
			matched = matched || ref.RefID == args.RepairOf
		}
		if !matched {
			return nil, errors.New("repair_of must match an evidence ref id")
		}
	}
	if args.WriteMode != "" && args.WriteMode != "standard" && args.WriteMode != "lossless_atomic" {
		return nil, errors.New("write_mode must be standard or lossless_atomic")
	}
	if args.OperationID == "" {
		var err error
		args.OperationID, err = newOperationID()
		if err != nil {
			return nil, err
		}
	}
	if args.EvidenceKind == "" {
		args.EvidenceKind = "agent_tool_claim"
	}
	metadata := make(map[string]any, len(args.Metadata)+1)
	for key, value := range args.Metadata {
		metadata[key] = value
	}
	if args.SourceSegments != nil {
		if _, exists := metadata["source_segments"]; exists {
			return nil, errors.New("source_segments supplied twice")
		}
		metadata["source_segments"] = args.SourceSegments
	}
	if args.Kind == "procedure" {
		if _, stamped := metadata["fingerprint"]; !stamped {
			fingerprint, err := selected.ProcedureDescriptor()
			if err != nil {
				return nil, err
			}
			metadata["fingerprint"] = fingerprint
		}
	}
	request := engine.CandidateRequest{OperationID: args.OperationID, Operation: operation,
		RepairOf: args.RepairOf, WriteMode: args.WriteMode, Content: args.Content,
		Title: args.Title, Kind: args.Kind, Tags: args.Tags, Metadata: metadata,
		EvidenceRefs: args.EvidenceRefs, RequestedBlockID: args.RequestedBlockID,
		SourceID: args.SourceID, EvidenceKind: args.EvidenceKind,
		UserID: args.UserID, ProjectID: args.ProjectID, SessionID: args.SessionID,
		TaskID: args.TaskID, SubtaskID: args.SubtaskID, ParentSessionID: args.ParentSessionID,
		RunID: args.RunID, StageID: args.StageID, ArtifactID: args.ArtifactID,
		ArtifactRevision: args.ArtifactRevision, ValidFrom: args.ValidFrom, ValidTo: args.ValidTo,
		RepositoryID: args.RepositoryID, Checkout: args.Checkout, Worktree: args.Worktree,
		Branch: args.Branch, Revision: args.Revision, DirtyState: args.DirtyState,
		Sensitivity: args.Sensitivity, ContentMode: args.ContentMode,
		OperationalRisk: args.OperationalRisk}
	result, err := selected.RememberCandidateFor(ctx, request, callerRole, protocolVersion)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"ok": result.Saved, "saved": result.Saved,
		"memory_id": result.RecordID, "operation_id": args.OperationID,
		"operation": operation, "requested_block_id": args.RequestedBlockID,
		"backend": backendID, "workspace_id": workspaceID,
		"route_decision": result.Route, "route_rejected": !result.Saved,
		"status": result.Route.Status, "verification_status": result.Route.VerificationStatus,
		"replayed": result.Replayed}
	if operation == "repair" {
		payload["repair_of"] = args.RepairOf
	}
	if !result.Saved {
		payload["reason"] = result.Route.Reason
		return payload, nil
	}
	record, err := selected.ReadStored(ctx, result.RecordID)
	if err != nil || record == nil {
		return nil, errors.New("committed candidate record is unavailable")
	}
	payload["actual_block_id"] = result.Route.BlockID
	payload["canonical_key"] = record.CanonicalKey
	payload["revision_id"] = record.RevisionID
	payload["recorded_at"] = record.RecordedAt
	payload["scope"] = record.Scope
	payload["status"] = record.Status
	payload["verification_status"] = record.VerificationStatus
	if len(result.ProvenanceEdges) > 0 {
		payload["provenance_edges"] = result.ProvenanceEdges
	}
	if args.WriteMode == "lossless_atomic" {
		for _, key := range []string{"write_mode", "summary_count", "source_segments", "source_bytes",
			"stored_bytes", "exact_source_coverage", "source_coverage_status", "exact_segment_fidelity"} {
			payload[key] = record.Metadata[key]
		}
	}
	scopeKey, err := record.ScopeKey()
	if err != nil {
		return nil, err
	}
	_, payload["scope_id"], _ = strings.Cut(scopeKey, ":")
	if result.Receipt != nil {
		payload["commit_revision"] = result.Receipt.CommitRevision
		payload["durable_status"] = "committed"
		if result.Receipt.ProjectionPending {
			payload["projection_status"] = "pending"
			payload["retryable"] = true
		} else {
			payload["projection_status"] = "committed"
			payload["projection_revision"] = result.Receipt.CommitRevision
			payload["retryable"] = false
		}
	}
	return payload, nil
}

func mcpHostLineage() map[string]string {
	sessionID := strings.TrimSpace(os.Getenv("UM_MEM_SESSION_ID"))
	if sessionID == "" || sessionID == "default" || sessionID == "host-mcp" {
		generated, err := newOperationID()
		if err == nil {
			sessionID = generated
		}
	}
	return map[string]string{"project_id": strings.TrimSpace(os.Getenv("UM_MEM_PROJECT_ID")),
		"task_id":           strings.TrimSpace(os.Getenv("UM_MEM_TASK_ID")),
		"subtask_id":        strings.TrimSpace(os.Getenv("UM_MEM_SUBTASK_ID")),
		"user_id":           strings.TrimSpace(os.Getenv("UM_MEM_USER_ID")),
		"parent_session_id": strings.TrimSpace(os.Getenv("UM_MEM_PARENT_SESSION_ID")),
		"run_id":            sessionID, "session_id": sessionID}
}

var openCodeProjectTwins = map[string]string{
	"stable_project_facts":    "project_scope_stable_project_facts",
	"decisions_and_contracts": "project_scope_decisions_and_contracts",
	"project_ideas":           "project_scope_project_ideas",
	"reflection_candidates":   "project_scope_reflection_candidates",
}

func routeOpenCodeProjectWrite(args *RememberArgs, blocks []memory.Block, projectID string) error {
	requested := strings.TrimSpace(args.RequestedBlockID)
	if requested == "" {
		return errors.New("OpenCode project-only writes require an explicit requested_block_id")
	}
	args.RequestedBlockID = requested
	byID := make(map[string]memory.Block, len(blocks))
	for _, block := range blocks {
		byID[block.BlockID] = block
	}
	block, exists := byID[requested]
	if exists && block.Scope != "workspace" {
		return nil
	}
	if projectID == "" {
		return errors.New("project-only memory writes require a bound project identity")
	}
	if exists && args.Operation != "" && args.Operation != "add" {
		return errors.New("OpenCode project-only routing cannot retarget a correction or repair from a workspace block")
	}
	if twinID, found := openCodeProjectTwins[requested]; found {
		target, present := byID[twinID]
		if !present || target.Scope != "project" {
			return errors.New("project-scoped memory route is unavailable for " + requested)
		}
		args.RequestedBlockID = twinID
		return nil
	}
	return errors.New("OpenCode project-only writes do not support workspace block " + requested)
}

func addRememberToolForHost(server *mcp.Server, selected *engine.Engine, workspaceID, backendID string, lineage map[string]string) {
	sessionID := lineage["session_id"]
	projectOnly := os.Getenv("UM_MEM_WRITE_SCOPE_PROJECT_ONLY") == "1"
	schema, err := jsonschema.For[RememberArgs](nil)
	if err != nil {
		panic(err)
	}
	schema.Properties["operation"].Enum = []any{"add", "correct", "repair"}
	schema.Properties["write_mode"].Enum = []any{"standard", "lossless_atomic"}
	mcp.AddTool(server, &mcp.Tool{Name: "remember", Description: "Add, repair, or correct memory in one writable logical block, including validated lossless_atomic source records.", InputSchema: schema},
		func(ctx context.Context, _ *mcp.CallToolRequest, args RememberArgs) (*mcp.CallToolResult, map[string]any, error) {
			if sessionID == "" {
				return nil, responseError(errors.New("MCP session identity is unavailable"), workspaceID, backendID), nil
			}
			for _, claim := range []struct{ provided, trusted string }{
				{args.ProjectID, lineage["project_id"]}, {args.TaskID, lineage["task_id"]},
				{args.SubtaskID, lineage["subtask_id"]}, {args.UserID, lineage["user_id"]},
				{args.ParentSessionID, lineage["parent_session_id"]},
				{args.RunID, lineage["run_id"]}, {args.SessionID, lineage["session_id"]},
			} {
				if claim.provided != "" && claim.provided != claim.trusted {
					return nil, responseError(errors.New("remember scope claim is not bound to MCP host lineage"), workspaceID, backendID), nil
				}
			}
			args.ProjectID, args.TaskID, args.SubtaskID = lineage["project_id"], lineage["task_id"], lineage["subtask_id"]
			args.UserID, args.RunID, args.SessionID = lineage["user_id"], sessionID, sessionID
			args.ParentSessionID = lineage["parent_session_id"]
			if projectOnly {
				if err := selected.RefreshCatalog(); err != nil {
					return nil, responseError(err, workspaceID, backendID), nil
				}
				if err := routeOpenCodeProjectWrite(&args, selected.Blocks(), lineage["project_id"]); err != nil {
					return nil, responseError(err, workspaceID, backendID), nil
				}
			}
			payload, err := RememberPayload(ctx, selected, args, workspaceID, backendID, "memory_client", 2)
			if err != nil {
				return nil, responseError(err, workspaceID, backendID), nil
			}
			return nil, payload, nil
		})
}
