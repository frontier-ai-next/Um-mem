package engine

import (
	"context"
	"errors"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

type VerificationLineage struct {
	UserID    string `json:"user_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	SubtaskID string `json:"subtask_id,omitempty"`
}

type VerificationSubjectRequest struct {
	MemoryID               string               `json:"memory_id"`
	ExpectedSourceRevision string               `json:"expected_source_revision_id"`
	Authority              string               `json:"authority,omitempty"`
	TargetBlockID          string               `json:"target_block_id,omitempty"`
	TargetScope            string               `json:"target_scope,omitempty"`
	TargetScopeID          string               `json:"target_scope_id,omitempty"`
	SourceEvidenceRefs     []memory.EvidenceRef `json:"source_evidence_refs,omitempty"`
	Lineage                VerificationLineage  `json:"lineage,omitempty"`
}

type CandidateVerificationSubject = memory.CandidateVerificationSubject

func exactSubjectIdentity(value string) bool {
	return value != "" && value == strings.TrimSpace(value)
}

func subjectRecordScopeID(record memory.MemoryRecord) string {
	switch record.Scope {
	case "workspace":
		return record.WorkspaceID
	case "project":
		return record.ProjectID
	case "user_global":
		return record.UserID
	case "session":
		return record.SessionID
	case "task":
		return record.TaskID
	case "subtask":
		return record.SubtaskID
	}
	return ""
}

func requirePromotionSourceLineage(source memory.MemoryRecord, lineage VerificationLineage) error {
	var bound string
	switch source.Scope {
	case "workspace":
		return nil
	case "project":
		bound = lineage.ProjectID
	case "user_global":
		bound = lineage.UserID
	case "session":
		bound = lineage.SessionID
	case "task":
		bound = lineage.TaskID
		if source.SessionID != "" && source.SessionID != lineage.SessionID {
			return errors.New("promotion source is outside caller session lineage")
		}
	case "subtask":
		bound = lineage.SubtaskID
		if source.TaskID != lineage.TaskID || source.SessionID != lineage.SessionID {
			return errors.New("promotion source is outside caller task lineage")
		}
	default:
		return errors.New("promotion source has an unsupported scope")
	}
	if !exactSubjectIdentity(bound) || bound != subjectRecordScopeID(source) {
		return errors.New("promotion source is outside caller scope lineage")
	}
	return nil
}

func (engine *Engine) VerificationSubject(ctx context.Context,
	request VerificationSubjectRequest) (CandidateVerificationSubject, error) {
	if !exactSubjectIdentity(request.MemoryID) ||
		!exactSubjectIdentity(request.ExpectedSourceRevision) {
		return CandidateVerificationSubject{}, errors.New("verification subject requires exact memory and source revision")
	}
	source, err := engine.ReadStored(ctx, request.MemoryID)
	if err != nil {
		return CandidateVerificationSubject{}, err
	}
	if source == nil || source.RevisionID != request.ExpectedSourceRevision {
		return CandidateVerificationSubject{}, errors.New("verification subject requires the exact source revision")
	}
	if err := requirePromotionSourceLineage(*source, request.Lineage); err != nil {
		return CandidateVerificationSubject{}, err
	}
	current, err := engine.index.IsCurrentRecord(ctx, *source)
	if err != nil {
		return CandidateVerificationSubject{}, err
	}
	if !current {
		return CandidateVerificationSubject{}, errors.New("verification subject requires the current canonical head")
	}
	if source.BlockMembership == nil {
		return CandidateVerificationSubject{}, errors.New("verification subject requires an exact leaf membership")
	}
	authority := request.Authority
	if authority == "" {
		authority = "advisory"
	}
	if authority != "advisory" && authority != "directive" {
		return CandidateVerificationSubject{}, errors.New("verification subject authority is unsupported")
	}
	targetBlockID := request.TargetBlockID
	targetScope := request.TargetScope
	if targetBlockID == "" {
		targetBlockID = source.BlockMembership.PrimaryBlockID
	} else {
		if !exactSubjectIdentity(targetBlockID) {
			return CandidateVerificationSubject{}, errors.New("publication target block must be exact")
		}
		if err := engine.RefreshCatalog(); err != nil {
			return CandidateVerificationSubject{}, err
		}
		block, ok := engine.current.Load().byID[targetBlockID]
		if !ok || block.IndexOnly || len(block.ChildIDs) != 0 {
			return CandidateVerificationSubject{}, errors.New("publication target requires a materialized leaf")
		}
		if request.TargetScope != "" && block.Scope != request.TargetScope {
			return CandidateVerificationSubject{}, errors.New("publication target scope does not match its leaf")
		}
		if targetScope == "" {
			targetScope = block.Scope
		}
	}
	scope, scopeID := targetScope, request.TargetScopeID
	if scope == "" {
		if scopeID != "" {
			return CandidateVerificationSubject{}, errors.New("target_scope_id requires target_scope")
		}
		scope, scopeID = source.Scope, subjectRecordScopeID(*source)
	} else {
		var boundID string
		switch scope {
		case "workspace":
			boundID = engine.config.WorkspaceID
		case "project":
			boundID = request.Lineage.ProjectID
		case "user_global":
			boundID = request.Lineage.UserID
		case "session":
			boundID = request.Lineage.SessionID
		case "task":
			boundID = request.Lineage.TaskID
		case "subtask":
			boundID = request.Lineage.SubtaskID
		default:
			return CandidateVerificationSubject{}, errors.New("promotion target_scope must name a supported exact scope")
		}
		if !exactSubjectIdentity(boundID) {
			return CandidateVerificationSubject{}, errors.New("promotion target scope is not bound to caller lineage")
		}
		if scopeID != "" && scopeID != boundID {
			return CandidateVerificationSubject{}, errors.New("promotion target_scope_id is not bound to caller lineage")
		}
		scopeID = boundID
	}
	for _, ref := range request.SourceEvidenceRefs {
		if err := ref.Validate(); err != nil {
			return CandidateVerificationSubject{}, err
		}
	}
	payloadHash, err := memory.PythonHashValue(*source)
	if err != nil {
		return CandidateVerificationSubject{}, err
	}
	evidenceRefs := request.SourceEvidenceRefs
	if evidenceRefs == nil {
		evidenceRefs = []memory.EvidenceRef{}
	}
	evidenceHash, err := memory.PythonHashValue(evidenceRefs)
	if err != nil {
		return CandidateVerificationSubject{}, err
	}
	result := CandidateVerificationSubject{WorkspaceID: engine.config.WorkspaceID,
		ProfileID: engine.config.ProfileID, MemoryID: source.BlockID,
		RevisionID: source.RevisionID, PayloadHash: payloadHash,
		SourceEvidenceHash: evidenceHash, TargetBlockID: targetBlockID,
		Authority: authority, Scope: scope, ScopeID: scopeID}
	if err := result.Validate(); err != nil {
		return CandidateVerificationSubject{}, err
	}
	return result, nil
}
