package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/frontier-ai-next/Um-mem/internal/catalog"
	"github.com/frontier-ai-next/Um-mem/internal/ledger"
	"github.com/frontier-ai-next/Um-mem/internal/receipts"
	"github.com/frontier-ai-next/Um-mem/memory"
)

type HostObservation struct {
	EventID          string   `json:"event_id"`
	EventKind        string   `json:"event_kind"`
	RawEvidence      string   `json:"raw_evidence"`
	ObservedAt       string   `json:"observed_at"`
	ProducerID       string   `json:"producer_id"`
	AtomicClaim      string   `json:"atomic_claim,omitempty"`
	SourcePath       string   `json:"source_path,omitempty"`
	SourceURI        string   `json:"source_uri,omitempty"`
	ArtifactHash     string   `json:"artifact_hash,omitempty"`
	ToolName         string   `json:"tool_name,omitempty"`
	WorkspaceRoot    string   `json:"workspace_root,omitempty"`
	ChangedFiles     []string `json:"changed_files,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	Speaker          string   `json:"speaker,omitempty"`
	SourceOccurredAt string   `json:"source_occurred_at,omitempty"`
}

type HostLineage struct {
	SessionID string
	TaskID    string
	SubtaskID string
	ProjectID string
}

var hostObservationKinds = map[string]bool{
	"conversation_turn": true, "file_created": true, "file_changed": true,
	"symbol_observed": true, "test_passed": true, "test_failed": true,
	"run_receipt": true, "trace_assertion": true, "source_opened": true,
	"source_snapshot": true, "source_claim": true, "session_episode": true,
	"handoff": true, "checkpoint": true, "report_created": true,
	"analysis_report": true, "decision": true,
}

func validateHostObservation(observation HostObservation, lineage HostLineage) error {
	if observation.EventID == "" || observation.EventID != strings.TrimSpace(observation.EventID) ||
		len(observation.EventID) > 256 || strings.ContainsRune(observation.EventID, 0) {
		return errors.New("host observation requires a bounded event_id")
	}
	if strings.TrimSpace(observation.RawEvidence) == "" || !utf8.ValidString(observation.RawEvidence) ||
		strings.TrimSpace(observation.ObservedAt) == "" ||
		observation.ProducerID == "" || observation.ProducerID != strings.TrimSpace(observation.ProducerID) {
		return errors.New("host observation requires raw_evidence, observed_at and producer_id")
	}
	if !hostObservationKinds[observation.EventKind] {
		return errors.New("host observation event_kind is unsupported")
	}
	if observation.EventKind != "conversation_turn" && strings.TrimSpace(observation.AtomicClaim) == "" {
		return errors.New("typed host observation requires an atomic_claim")
	}
	if len([]rune(observation.AtomicClaim)) > 512 {
		return errors.New("host observation atomic_claim exceeds 512 characters")
	}
	if lineage.SessionID == "" || lineage.SessionID != strings.TrimSpace(lineage.SessionID) {
		return errors.New("host observation requires a bound session_id")
	}
	return nil
}

func hostObservationMatches(record memory.MemoryRecord, observation HostObservation,
	lineage HostLineage, rawHash string) bool {
	fingerprint, err := hostObservationFingerprint(observation)
	if err != nil {
		return false
	}
	if stored, ok := record.Metadata["observation_fingerprint"].(string); ok {
		if stored != fingerprint {
			return false
		}
	} else if observation.EventKind != "conversation_turn" || observation.AtomicClaim != "" ||
		observation.SourcePath != "" || observation.SourceURI != "" ||
		observation.ArtifactHash != "" || observation.WorkspaceRoot != "" ||
		len(observation.ChangedFiles) > 0 || len(observation.Tags) > 0 {
		// Python-era conversation witnesses have no request fingerprint. Their
		// exact bytes, scope and producer remain independently checkable below.
		return false
	}
	if record.Kind != "raw_host_witness" || record.Status != "candidate" ||
		record.Retention != "ephemeral" || record.DeliveryPolicy != "hidden" ||
		record.BlockMembership == nil || record.BlockMembership.PrimaryBlockID != "transient_logs" ||
		record.Scope != "session" || record.Content != observation.RawEvidence ||
		record.SessionID != lineage.SessionID || record.TaskID != lineage.TaskID ||
		record.SubtaskID != lineage.SubtaskID || record.ProjectID != lineage.ProjectID ||
		record.Metadata["host_event_id"] != observation.EventID ||
		record.Metadata["host_event_kind"] != observation.EventKind ||
		record.Metadata["raw_sha256"] != rawHash ||
		stringValue(record.Metadata["tool_name"]) != observation.ToolName ||
		stringValue(record.Metadata["source_speaker"]) != observation.Speaker ||
		stringValue(record.Metadata["source_occurred_at"]) != observation.SourceOccurredAt ||
		len(record.EvidenceRefs) != 1 {
		return false
	}
	ref := record.EvidenceRefs[0]
	return ref.RefType == "session_event" && ref.RefID == observation.EventID &&
		ref.ProducedBy == "watcher" && ref.ProducerID == observation.ProducerID &&
		ref.Hash != nil && *ref.Hash == rawHash
}

func hostObservationFingerprint(observation HostObservation) (string, error) {
	observation.ObservedAt = ""
	encoded, err := json.Marshal(observation)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func normalizeObservationRoot(observation *HostObservation) error {
	if observation.WorkspaceRoot == "" {
		return nil
	}
	root, err := filepath.Abs(observation.WorkspaceRoot)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return errors.New("test observation workspace_root must be an existing directory")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return errors.New("test observation workspace_root must be an existing directory")
	}
	observation.WorkspaceRoot = root
	return nil
}

// ObserveHostFor adds the local service's durable correlation receipt. The
// observed_at clock is intentionally excluded from request identity so a host
// can retry the same event after restart without inventing a new source.
func (engine *Engine) ObserveHostFor(ctx context.Context, observation HostObservation,
	lineage HostLineage, operationID, callerRole string, protocolVersion int) (map[string]any, error) {
	if callerRole != "host_hook" || operationID == "" || protocolVersion < 1 {
		return nil, errors.New("host observation requires a bound host_hook operation")
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		return nil, err
	}
	var request map[string]any
	if err := json.Unmarshal(encoded, &request); err != nil {
		return nil, err
	}
	delete(request, "observed_at")
	arguments := map[string]any{"tool": "observe", "request": request,
		"lineage": map[string]any{"session_id": lineage.SessionID, "task_id": lineage.TaskID,
			"subtask_id": lineage.SubtaskID, "project_id": lineage.ProjectID}}
	identity := receipts.Identity{WorkspaceID: engine.config.WorkspaceID,
		ProfileID: engine.config.ProfileID, Backend: engine.config.BackendID,
		CallerRole: callerRole, ProtocolVersion: protocolVersion}
	claimed, err := receipts.Claim(ctx, engine.config.RepoRoot, identity, operationID, arguments)
	if err != nil {
		return nil, err
	}
	if claimed.Status == "committed" {
		if claimed.Response["receipt_kind"] != "mutation_response" ||
			claimed.Response["mutation_tool"] != "observe" {
			return nil, errors.New("host observation receipt response is invalid")
		}
		response, ok := claimed.Response["response"].(map[string]any)
		if !ok || response["ok"] != true {
			return nil, errors.New("host observation receipt response is invalid")
		}
		witnessID, _ := response["witness_id"].(string)
		witness, err := engine.ReadStored(ctx, witnessID)
		if err != nil || witness == nil {
			return nil, errors.New("host observation receipt witness is unavailable")
		}
		if err := engine.requireCurrentRawWitness(ctx, *witness); err != nil {
			return nil, err
		}
		response["replayed"] = true
		return response, nil
	}
	response, err := engine.ObserveHost(ctx, observation, lineage)
	if err != nil {
		checksum := sha256.Sum256([]byte(err.Error()))
		failure := map[string]any{"ok": false, "error_type": "GoError",
			"reason_code": "host_observation_failed", "error_sha256": hex.EncodeToString(checksum[:])}
		if completeErr := receipts.Complete(ctx, engine.config.RepoRoot, identity,
			operationID, arguments, failure, "failed"); completeErr != nil {
			return nil, errors.Join(err, completeErr)
		}
		return nil, err
	}
	receipt := map[string]any{"receipt_kind": "mutation_response", "mutation_tool": "observe",
		"response": response}
	if err := receipts.Complete(ctx, engine.config.RepoRoot, identity,
		operationID, arguments, receipt, "committed"); err != nil {
		return nil, err
	}
	return response, nil
}

// ObserveHost records exact host bytes in the hidden transient block. The
// local-service dispatcher binds lineage and restricts this path to host_hook.
func (engine *Engine) ObserveHost(ctx context.Context, observation HostObservation,
	lineage HostLineage) (map[string]any, error) {
	if err := validateHostObservation(observation, lineage); err != nil {
		return nil, err
	}
	if err := normalizeObservationRoot(&observation); err != nil {
		return nil, err
	}
	fingerprint, err := hostObservationFingerprint(observation)
	if err != nil {
		return nil, err
	}
	if err := engine.RefreshCatalog(); err != nil {
		return nil, err
	}
	block, exists := engine.current.Load().byID["transient_logs"]
	if !exists || block.Producer != "host_observer" || block.IndexOnly ||
		block.Scope != "session" || block.Retention != "ephemeral" ||
		block.DeliveryPolicy != "hidden" || block.Authority != "advisory" {
		return nil, errors.New("host witness block contract is unavailable")
	}
	digest := sha256.Sum256([]byte(observation.RawEvidence))
	rawHash := "sha256:" + hex.EncodeToString(digest[:])
	canonicalKey, err := memory.DeriveCanonicalKey(engine.config.WorkspaceID,
		engine.config.ProfileID, "raw_host_witness",
		"host:"+observation.EventID+":raw_host_witness")
	if err != nil {
		return nil, err
	}
	recordID := memory.BlockIDForCanonicalKey(canonicalKey)
	existing, err := engine.index.ReadStoredRecord(ctx, recordID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if !hostObservationMatches(*existing, observation, lineage, rawHash) {
			return nil, errors.New("registered_source_conflict")
		}
		if err := engine.requireCurrentRawWitness(ctx, *existing); err != nil {
			return nil, err
		}
		return engine.hostObservationReceipt(ctx, observation, recordID, rawHash, true)
	}
	// A previous call may have written the immutable record before native
	// projection completed. Retry that exact journal row instead of replacing it.
	rows, err := engine.index.ProjectionRows(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Record.BlockID != recordID || row.Backend != engine.recordNativeStore(row.Record) || row.Status != "pending" {
			continue
		}
		if !hostObservationMatches(row.Record, observation, lineage, rawHash) {
			return nil, errors.New("registered_source_conflict")
		}
		if _, err := engine.writer.CompleteProjection(ctx, row.Revision, engine.projector); err != nil {
			return nil, err
		}
		return engine.hostObservationReceipt(ctx, observation, recordID, rawHash, true)
	}
	record, err := memory.NewMemoryRecord(observation.RawEvidence, engine.config.WorkspaceID)
	if err != nil {
		return nil, err
	}
	record.BlockID = recordID
	record.ProfileID, record.Kind = engine.config.ProfileID, "raw_host_witness"
	record.Title = "Raw host witness " + observation.EventID
	if len([]rune(record.Title)) > 96 {
		record.Title = string([]rune(record.Title)[:96])
	}
	record.Status, record.Retention, record.DeliveryPolicy = "candidate", block.Retention, block.DeliveryPolicy
	record.Temperature, record.Authority = block.Temperature, "advisory"
	record.TrustLevel, record.VerificationStatus = "source_observed", "observed"
	record.Scope, record.Producer, record.SourceBackend = "session", "host_observer", "um_mem"
	record.SessionID, record.TaskID = lineage.SessionID, lineage.TaskID
	record.SubtaskID, record.ProjectID, record.RunID = lineage.SubtaskID, lineage.ProjectID, lineage.SessionID
	record.Tags = append(append([]string{}, block.Tags...), "raw_witness", observation.EventKind)
	record.EvidenceRefs = []memory.EvidenceRef{{RefType: "session_event", RefID: observation.EventID,
		Hash: &rawHash, ProducedBy: "watcher", ProducerID: observation.ProducerID}}
	record.Metadata = map[string]any{"record_kind": "raw_host_witness",
		"host_event_id": observation.EventID, "host_event_kind": observation.EventKind,
		"observed_at": observation.ObservedAt, "raw_sha256": rawHash,
		"tool_name": observation.ToolName, "observation_fingerprint": fingerprint}
	if observation.Speaker != "" {
		record.Metadata["source_speaker"] = observation.Speaker
	}
	if observation.SourceOccurredAt != "" {
		record.Metadata["source_occurred_at"] = observation.SourceOccurredAt
	}
	record.CanonicalKey = canonicalKey
	record.DuplicateClusterID, err = memory.DuplicateClusterID(record)
	if err != nil {
		return nil, err
	}
	partition := memory.StaticBackendPartitionID(engine.config.ProfileID, block.BlockID)
	record.BlockMembership = &memory.RecordBlockMembership{PrimaryBlockID: block.BlockID,
		SecondaryBlockIDs: []string{}, CatalogRevisionID: engine.CatalogRevisionID(),
		BackendPartitionID: partition}
	record.RecordedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
	record.RevisionID, err = memory.RevisionIDForRecord(canonicalKey, record, "insert", "")
	if err != nil {
		return nil, err
	}
	record.BlockMembership = nil
	if err := engine.bindHostWitness(&record, block.BlockID, partition); err != nil {
		return nil, err
	}
	staged, err := engine.writer.PrepareRootWrite(ctx, record)
	if err != nil {
		return nil, err
	}
	if _, err := engine.writer.CompleteProjection(ctx, staged.CommitRevision, engine.projector); err != nil {
		return nil, err
	}
	return engine.hostObservationReceipt(ctx, observation, recordID, rawHash, staged.Replayed)
}

func (engine *Engine) bindHostWitness(record *memory.MemoryRecord, blockID, partition string) error {
	if record == nil || record.BlockMembership != nil || record.ProfileID != engine.config.ProfileID ||
		record.WorkspaceID != engine.config.WorkspaceID {
		return errors.New("host witness binding has invalid scope")
	}
	membership := memory.RecordBlockMembership{PrimaryBlockID: blockID,
		SecondaryBlockIDs: []string{}, CatalogRevisionID: engine.CatalogRevisionID(),
		BackendPartitionID: partition, RecordID: record.BlockID,
		RecordRevisionID: record.RevisionID, WorkspaceID: record.WorkspaceID,
		ValidFrom: record.ValidFrom}
	membership.MembershipRevisionID = membership.RevisionHash()
	record.BlockMembership = &membership
	record.SemanticCatalogVersion = membership.CatalogRevisionID
	return record.ValidateForStorage()
}

func (engine *Engine) hostObservationReceipt(ctx context.Context, observation HostObservation,
	witnessID, rawHash string, replayed bool) (map[string]any, error) {
	ref := memory.EvidenceRef{RefType: "artifact", RefID: witnessID,
		Hash: &rawHash, ProducedBy: "watcher", ProducerID: observation.ProducerID}
	candidates := make([]map[string]any, 0)
	if observation.EventKind != "conversation_turn" {
		opened, err := catalog.Open(engine.config.RepoRoot, engine.config.WorkspaceID, engine.config.ProfileID)
		if err != nil {
			return nil, err
		}
		states, err := opened.ListBlocks(ctx, false)
		opened.Close()
		if err != nil {
			return nil, err
		}
		parents := make(map[string]bool)
		for _, state := range states {
			if state.Block.ParentID != "" {
				parents[state.Block.ParentID] = true
			}
			if len(state.Block.ChildIDs) > 0 {
				parents[state.Block.BlockID] = true
			}
		}
		for _, state := range states {
			block := state.Block
			if state.Status != "active" || parents[block.BlockID] || block.IndexOnly ||
				!block.AgentWritable || block.Retention != "durable" {
				continue
			}
			candidates = append(candidates, map[string]any{"block_id": block.BlockID,
				"use_when": block.Guidance["use_when"], "avoid_when": block.Guidance["avoid_when"]})
		}
	}
	manifest := map[string]any{"requires_one_writable_leaf": observation.EventKind != "conversation_turn",
		"witness_id": witnessID, "candidate_blocks": candidates}
	outcome, reason := "needs_agent_choice", "semantic_placement_requires_explicit_agent_choice"
	if observation.EventKind == "conversation_turn" {
		outcome, reason = "raw_witness_only", "conversation_turn_has_no_automatic_semantic_projection"
	}
	response := map[string]any{"ok": true, "saved": true, "backend": engine.config.BackendID,
		"workspace_id": engine.config.WorkspaceID, "witness_id": witnessID,
		"memory_id": "", "requested_block_id": "", "actual_block_id": "",
		"route_outcome": outcome, "reason": reason, "evidence_refs": []memory.EvidenceRef{ref},
		"placement_manifest": manifest, "status": "observed", "authority": "advisory",
		"verification_status": "observed", "replayed": replayed}
	if observation.EventKind == "conversation_turn" {
		response["source_id"] = observation.EventID
		response["source_sha256"] = strings.TrimPrefix(rawHash, "sha256:")
		response["source_bytes"] = len([]byte(observation.RawEvidence))
	}
	if (observation.EventKind == "test_passed" || observation.EventKind == "test_failed") &&
		observation.WorkspaceRoot != "" {
		root, err := filepath.Abs(observation.WorkspaceRoot)
		if err != nil {
			return nil, err
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return nil, errors.New("test observation workspace_root must be an existing directory")
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			return nil, errors.New("test observation workspace_root must be an existing directory")
		}
		stageID := "host_observation:" + observation.EventID
		reportHash := observation.ArtifactHash
		if reportHash == "" {
			reportHash = rawHash
		}
		passed := observation.EventKind == "test_passed"
		prior, err := ledger.FindVerificationReport(engine.config.RepoRoot, engine.config.WorkspaceID,
			stageID, observation.ProducerID, reportHash, passed)
		if err != nil {
			return nil, err
		}
		var report ledger.VerificationReport
		if prior != nil {
			report = *prior
		} else {
			report, err = ledger.AppendVerificationReport(engine.config.RepoRoot,
				engine.config.WorkspaceID, observation.ProducerID, reportHash, stageID,
				root, observation.ChangedFiles, passed)
			if err != nil {
				return nil, err
			}
		}
		response["verification_report"] = report
	}
	return response, nil
}
