package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/memory"
	_ "modernc.org/sqlite"
)

type Writer struct {
	db          *sql.DB
	path        string
	workspaceID string
	profileID   string
	backendID   string
}

type WriteReceipt struct {
	RecordID          string `json:"record_id"`
	CommitRevision    int64  `json:"commit_revision"`
	ProjectionPending bool   `json:"projection_pending"`
	Replayed          bool   `json:"replayed"`
}

func OpenWriter(path, workspaceID, profileID, backendID string) (*Writer, error) {
	if !oneBackend(backendID) || workspaceID == "" || profileID == "" {
		return nil, errors.New("writer requires workspace, profile, and one selected backend")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(absPath); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteFileURI(absPath, "rw"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout=10000; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON;"); err != nil {
		db.Close()
		return nil, err
	}
	var version int
	err = db.QueryRow("SELECT value FROM schema_metadata WHERE key='schema_version'").Scan(&version)
	if err == nil && version != IndexSchemaVersion {
		err = fmt.Errorf("unsupported canonical index schema version %d", version)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	for _, statement := range []string{
		// Additive version-16 index for existing stores: ProjectionRows and
		// exact reads need the latest journal revision regardless of status.
		`CREATE INDEX IF NOT EXISTS idx_write_outbox_block_revision
		ON write_outbox(block_id,commit_revision)`,
		`CREATE INDEX IF NOT EXISTS idx_native_partition_events_stable_partition
		ON native_partition_events(workspace_id,primary_block_id,partition_id,id)`,
		`CREATE INDEX IF NOT EXISTS idx_native_partition_events_partition_latency
		ON native_partition_events(workspace_id,primary_block_id,partition_id,latency_ms,id)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Writer{db: db, path: absPath, workspaceID: workspaceID, profileID: profileID, backendID: backendID}, nil
}

func oneBackend(backendID string) bool {
	switch backendID {
	case "mempalace", "hindsight", "mnemosyne", "zvec":
		return true
	default:
		return false
	}
}

func (writer *Writer) Close() error { return writer.db.Close() }

func (writer *Writer) outboxStore(record memory.MemoryRecord) string {
	if writer.backendID == "mempalace" {
		return memory.MemPalaceStoreForBlock(record)
	}
	return writer.backendID
}

func canonicalRecordJSON(record memory.MemoryRecord) (string, error) {
	encoded, err := jsoncompat.MarshalPython(record, false)
	return string(encoded), err
}

func sameJSON(left, right string) bool {
	var a, b any
	leftDecoder := json.NewDecoder(strings.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(strings.NewReader(right))
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&a) != nil || rightDecoder.Decode(&b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

func sha256Hex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// PrepareRootWrite atomically stores a new canonical root and its native
// projection journal. Native projection must be completed by the selected
// backend before this record becomes visible to ordinary exact reads.
func (writer *Writer) PrepareRootWrite(ctx context.Context, record memory.MemoryRecord) (WriteReceipt, error) {
	return writer.prepareRootWrite(ctx, record, nil, nil)
}

// PrepareConsolidationRootWrite stages the canonical record and its verifier
// receipt under one mutation identity before native projection.
func (writer *Writer) PrepareConsolidationRootWrite(ctx context.Context, record memory.MemoryRecord,
	consolidation memory.ConsolidationCommitReceipt) (WriteReceipt, error) {
	if record.CanonicalKey == "" {
		return WriteReceipt{}, errors.New("consolidation root requires a canonical key")
	}
	return writer.prepareRootWrite(ctx, record, &consolidation, nil)
}

// PreparePublicationRootWrite stages a verified copy into a distinct leaf.
// Its source head is checked again when native projection commits the copy.
func (writer *Writer) PreparePublicationRootWrite(ctx context.Context, record memory.MemoryRecord, publication memory.PublicationCommitReceipt) (WriteReceipt, error) {
	if !publication.MaterializedCopy || record.CanonicalKey == "" {
		return WriteReceipt{}, errors.New("publication root requires a materialized copy with a canonical key")
	}
	return writer.prepareRootWrite(ctx, record, nil, &publication)
}

func (writer *Writer) prepareRootWrite(ctx context.Context, record memory.MemoryRecord,
	consolidation *memory.ConsolidationCommitReceipt, publication *memory.PublicationCommitReceipt) (WriteReceipt, error) {
	if record.WorkspaceID != writer.workspaceID || record.ProfileID != writer.profileID {
		return WriteReceipt{}, errors.New("canonical write identity does not match selected workspace and profile")
	}
	if record.RevisionOf != "" || len(record.SupersedesIDs) != 0 || !oneOfStatus(record.Status) {
		return WriteReceipt{}, errors.New("root write requires a new candidate or accepted record")
	}
	if record.CanonicalKey != strings.TrimSpace(record.CanonicalKey) {
		return WriteReceipt{}, errors.New("canonical key must be an exact trimmed identity")
	}
	if err := record.ValidateForStorage(); err != nil {
		return WriteReceipt{}, err
	}
	if publication != nil {
		if err := publication.ValidateForRecord(record); err != nil {
			return WriteReceipt{}, err
		}
	}
	if consolidation != nil {
		if publication != nil {
			return WriteReceipt{}, errors.New("root mutation cannot combine consolidation and publication receipts")
		}
		if err := consolidation.ValidateForRecord(record); err != nil {
			return WriteReceipt{}, err
		}
	}
	payload, err := canonicalRecordJSON(record)
	if err != nil {
		return WriteReceipt{}, err
	}
	checksum := sha256Hex("record\x00" + payload)
	payloadID := "record:" + checksum
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := writer.db.BeginTx(ctx, nil)
	if err != nil {
		return WriteReceipt{}, err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT payload_json FROM blocks WHERE block_id=?", record.BlockID).Scan(&existing)
	if err != nil && err != sql.ErrNoRows {
		return WriteReceipt{}, err
	}
	if err == nil {
		if !sameJSON(existing, payload) {
			return WriteReceipt{}, errors.New("canonical memory revisions are immutable")
		}
		var revision int64
		var status, backend, workspace string
		if err := tx.QueryRowContext(ctx,
			"SELECT commit_revision,status,store,workspace_id FROM write_outbox WHERE block_id=? ORDER BY commit_revision DESC LIMIT 1",
			record.BlockID,
		).Scan(&revision, &status, &backend, &workspace); err != nil {
			return WriteReceipt{}, err
		}
		if backend != writer.outboxStore(record) || workspace != writer.workspaceID {
			return WriteReceipt{}, errors.New("selected backend write identity conflicts with existing journal")
		}
		if publication != nil || record.CanonicalKey != "" {
			var stagedJSON string
			if err := tx.QueryRowContext(ctx, `SELECT p.payload_json FROM canonical_head_outbox h
				JOIN canonical_payloads p ON p.payload_id=h.payload_id
				WHERE h.successor_id=? ORDER BY h.commit_revision DESC LIMIT 1`, record.BlockID).Scan(&stagedJSON); err != nil {
				return WriteReceipt{}, err
			}
			var staged stagedSuccessorMutation
			if err := json.Unmarshal([]byte(stagedJSON), &staged); err != nil {
				return WriteReceipt{}, err
			}
			if err := samePublicationReceipt(staged.PublicationReceipt, publication); err != nil {
				return WriteReceipt{}, err
			}
			if err := sameConsolidationReceipt(staged.ConsolidationReceipt, consolidation); err != nil {
				return WriteReceipt{}, err
			}
		}
		return WriteReceipt{record.BlockID, revision, status != "committed", true}, nil
	}
	if publication != nil {
		if err := validatePublicationSourceTx(ctx, tx, *publication); err != nil {
			return WriteReceipt{}, err
		}
		if err := validatePublicationCopyIdentityTx(ctx, tx, *publication, record); err != nil {
			return WriteReceipt{}, err
		}
		if err := ensurePublicationReceiptUnused(ctx, tx, publication.ReceiptID); err != nil {
			return WriteReceipt{}, err
		}
	}
	if consolidation != nil {
		if err := ensureConsolidationReceiptUnused(ctx, tx, consolidation.ReceiptID); err != nil {
			return WriteReceipt{}, err
		}
		if err := validateConsolidationSourcesTx(ctx, tx, record); err != nil {
			return WriteReceipt{}, err
		}
	}
	if record.CanonicalKey != "" {
		var head string
		err = tx.QueryRowContext(ctx,
			"SELECT block_id FROM canonical_heads WHERE workspace_id=? AND canonical_key_sha256=?",
			record.WorkspaceID, sha256Hex(record.CanonicalKey),
		).Scan(&head)
		if err != nil && err != sql.ErrNoRows {
			return WriteReceipt{}, err
		}
		if head != "" && head != record.BlockID {
			return WriteReceipt{}, errors.New("canonical key already has a current head")
		}
	}
	keyHash := ""
	if record.CanonicalKey != "" {
		keyHash = sha256Hex(record.CanonicalKey)
	}
	revision, err := writer.insertStagedRecordTx(ctx, tx, record, payload, payloadID, checksum, keyHash, now)
	if err != nil {
		return WriteReceipt{}, err
	}
	if record.CanonicalKey != "" {
		mutationJSON, err := canonicalMutationJSONWithReceipts(payloadID, checksum, "insert", "", "", nil, consolidation, publication)
		if err != nil {
			return WriteReceipt{}, err
		}
		mutationChecksum := sha256Hex("mutation\x00" + mutationJSON)
		mutationID := "mutation:" + mutationChecksum
		if _, err := tx.ExecContext(ctx, `INSERT INTO canonical_payloads(
			payload_id,payload_sha256,payload_kind,payload_json,created_at
		) VALUES(?,?,?,?,?)`, mutationID, mutationChecksum, "mutation", mutationJSON, now); err != nil {
			return WriteReceipt{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO canonical_head_outbox(
			workspace_id,canonical_key_sha256,predecessor_id,successor_id,
			successor_status,successor_authority,mutation_action,rollback_target_id,
			payload_id,payload_sha256,status,error,created_at,updated_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,'pending','',?,?)`,
			record.WorkspaceID, keyHash, "", record.BlockID, record.Status,
			record.Authority, "insert", "", mutationID, mutationChecksum, now, now,
		); err != nil {
			return WriteReceipt{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return WriteReceipt{}, err
	}
	return WriteReceipt{record.BlockID, revision, true, false}, nil
}

func (writer *Writer) insertStagedRecordTx(ctx context.Context, tx *sql.Tx, record memory.MemoryRecord, payload, payloadID, checksum, keyHash, now string) (int64, error) {
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO canonical_payloads(payload_id,payload_sha256,payload_kind,payload_json,created_at) VALUES(?,?,?,?,?)",
		payloadID, checksum, "record", payload, now,
	); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO blocks(
		block_id,workspace_id,run_id,stage_id,subtask_id,scope,status,retention,
		delivery_policy,temperature,authority,trust_level,verification_status,
		promotion_decision,content,payload_json,updated_at,committed_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		record.BlockID, record.WorkspaceID, record.RunID, record.StageID, record.SubtaskID,
		record.Scope, record.Status, record.Retention, record.DeliveryPolicy, record.Temperature,
		record.Authority, record.TrustLevel, record.VerificationStatus, record.PromotionDecision,
		record.Content, payload, record.UpdatedAt, "",
	); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memory_temporal_index(workspace_id,block_id,canonical_key_sha256,revision_id,
		valid_from,valid_to,recorded_at,is_current) VALUES(?,?,?,?,?,?,?,1)`,
		record.WorkspaceID, record.BlockID, keyHash, record.RevisionID,
		record.ValidFrom, record.ValidTo, record.RecordedAt,
	); err != nil {
		return 0, err
	}
	membership := record.BlockMembership
	blocks := append([]string{membership.PrimaryBlockID}, membership.SecondaryBlockIDs...)
	for ordinal, blockID := range blocks {
		role := "secondary"
		if ordinal == 0 {
			role = "primary"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO record_block_memberships(
			workspace_id,record_id,record_revision_id,membership_revision_id,logical_block_id,
			role,ordinal,catalog_revision_id,backend_partition_id,valid_from,valid_to,is_current
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`,
			record.WorkspaceID, record.BlockID, record.RevisionID, membership.MembershipRevisionID,
			blockID, role, ordinal, membership.CatalogRevisionID, membership.BackendPartitionID,
			membership.ValidFrom, membership.ValidTo,
		); err != nil {
			return 0, err
		}
	}
	document, err := memory.CompileIndexDocument(record)
	if err != nil {
		return 0, err
	}
	for _, term := range document.ExactTerms {
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_exact_terms(
			workspace_id,term_type,term_value,block_id) VALUES(?,?,?,?)`,
			record.WorkspaceID, term.Type, term.Value, record.BlockID); err != nil {
			return 0, err
		}
	}
	for _, link := range document.ProvenanceLinks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_provenance_links(
			workspace_id,derived_id,raw_source_ref,block_id) VALUES(?,?,?,?)`,
			record.WorkspaceID, link.DerivedID, link.RawSourceRef, record.BlockID); err != nil {
			return 0, err
		}
	}
	for _, node := range document.StructureNodes {
		if err := upsertIndexNodeTx(ctx, tx, node); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO memory_fts(block_id,workspace_id,title,content,tags) VALUES(?,?,?,?,?)",
		record.BlockID, record.WorkspaceID, record.Title, record.Content, strings.Join(record.Tags, " "),
	); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO write_outbox(
		block_id,workspace_id,store,payload_id,payload_sha256,status,error,created_at,updated_at
	) VALUES(?,?,?,?,?,'pending','',?,?)`,
		record.BlockID, record.WorkspaceID, writer.outboxStore(record), payloadID, checksum, now, now,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func oneOfStatus(status string) bool { return status == "candidate" || status == "accepted" }
