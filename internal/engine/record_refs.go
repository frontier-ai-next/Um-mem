package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// PinnedRecordRef names one current authorized revision without content.
type PinnedRecordRef struct {
	RecordID       string `json:"record_id"`
	RevisionID     string `json:"revision_id"`
	ContentHash    string `json:"content_hash"`
	LogicalBlockID string `json:"logical_block_id"`
	Status         string `json:"status"`
}

// WorkingRecordSnapshot contains complete candidate content for an exact
// owner, generation, revision and content digest. Private metadata is absent.
type WorkingRecordSnapshot struct {
	Reference    PinnedRecordRef      `json:"reference"`
	Title        string               `json:"title"`
	Content      string               `json:"content"`
	Kind         string               `json:"kind"`
	Scope        string               `json:"scope"`
	EvidenceRefs []memory.EvidenceRef `json:"evidence_refs"`
}

func (engine *Engine) PinRecord(ctx context.Context, recordID, ownerID, generationID string,
	scopeChain []string) (PinnedRecordRef, error) {
	if strings.TrimSpace(recordID) == "" || len(scopeChain) == 0 {
		return PinnedRecordRef{}, errors.New("memory record is unavailable in the current scope and generation")
	}
	record, err := engine.ReadStored(ctx, recordID)
	if err != nil {
		return PinnedRecordRef{}, err
	}
	if record == nil || record.BlockID != recordID ||
		record.WorkspaceID != engine.config.WorkspaceID || record.ProfileID != engine.config.ProfileID ||
		(record.Status != "candidate" && record.Status != "accepted") {
		return PinnedRecordRef{}, errors.New("memory record is unavailable in the current scope and generation")
	}
	candidateOwned := record.Status == "candidate" &&
		strings.TrimSpace(ownerID) != "" && strings.TrimSpace(generationID) != "" &&
		ownedCandidate(*record, ownerID, generationID)
	if record.Status == "candidate" && !candidateOwned {
		return PinnedRecordRef{}, errors.New("memory record is unavailable in the current scope and generation")
	}
	plan := memory.NewRecallPlan("")
	plan.WorkspaceID, plan.ProfileID = engine.config.WorkspaceID, engine.config.ProfileID
	plan.ScopeChain, plan.RunID = scopeChain, record.RunID
	plan.IncludeCandidates, plan.SourceHorizon = candidateOwned, "durable"
	current, err := engine.index.IsCurrentRecord(ctx, *record)
	if err != nil {
		return PinnedRecordRef{}, err
	}
	if !current || !memory.EligibleForRecall(*record, plan, time.Now().UTC()) {
		return PinnedRecordRef{}, errors.New("memory record is unavailable in the current scope and generation")
	}
	if record.Status == "accepted" {
		dependencies, err := engine.index.ValidateDependencies(ctx, recordID, store.DependencyOptions{})
		if err != nil {
			return PinnedRecordRef{}, err
		}
		if dependencies.Status != "valid" {
			return PinnedRecordRef{}, errors.New("memory record is unavailable because its dependencies are not current")
		}
	}
	digest := sha256.Sum256([]byte(record.Content))
	logicalBlockID := ""
	if record.BlockMembership != nil {
		logicalBlockID = record.BlockMembership.PrimaryBlockID
	}
	return PinnedRecordRef{RecordID: record.BlockID, RevisionID: record.RevisionID,
		ContentHash: hex.EncodeToString(digest[:]), LogicalBlockID: logicalBlockID,
		Status: record.Status}, nil
}

func (engine *Engine) SnapshotWorkingRecord(ctx context.Context, recordID, ownerID,
	generationID, expectedRevisionID, expectedContentHash string, maxTokens, maxBytes int,
	scopeChain []string) (WorkingRecordSnapshot, error) {
	if strings.TrimSpace(ownerID) == "" || strings.TrimSpace(generationID) == "" ||
		strings.TrimSpace(expectedRevisionID) == "" {
		return WorkingRecordSnapshot{}, errors.New("working snapshot requires an exact owner, generation and revision")
	}
	if len(expectedContentHash) != 64 || strings.Trim(expectedContentHash, "0123456789abcdef") != "" {
		return WorkingRecordSnapshot{}, errors.New("working snapshot requires an exact SHA-256 content hash")
	}
	if maxTokens < 1 || maxBytes < 1 {
		return WorkingRecordSnapshot{}, errors.New("working snapshot budgets must be positive integers")
	}
	reference, err := engine.PinRecord(ctx, recordID, ownerID, generationID, scopeChain)
	if err != nil {
		return WorkingRecordSnapshot{}, err
	}
	record, err := engine.ReadStored(ctx, recordID)
	if err != nil {
		return WorkingRecordSnapshot{}, err
	}
	if record == nil || reference.Status != "candidate" || reference.RevisionID != expectedRevisionID ||
		reference.ContentHash != expectedContentHash || record.ContentMode != "full" {
		return WorkingRecordSnapshot{}, errors.New("working record snapshot is unavailable at the exact requested revision")
	}
	snapshot := WorkingRecordSnapshot{Reference: reference, Title: record.Title,
		Content: record.Content, Kind: record.Kind, Scope: record.Scope,
		EvidenceRefs: append([]memory.EvidenceRef{}, record.EvidenceRefs...)}
	if utf8.RuneCountInString(record.Content)+utf8.RuneCountInString(record.Title) > maxBytes {
		return WorkingRecordSnapshot{}, errors.New("complete working record snapshot exceeds its explicit content budget")
	}
	encoded, err := jsoncompat.MarshalPython(snapshot, false)
	if err != nil {
		return WorkingRecordSnapshot{}, err
	}
	if len(encoded) > maxBytes || memory.EstimateTokens(string(encoded)) > maxTokens {
		return WorkingRecordSnapshot{}, errors.New("complete working record snapshot exceeds its explicit content budget")
	}
	return snapshot, nil
}
