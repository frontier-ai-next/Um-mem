package mnemosyne

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/paths"
	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
	_ "modernc.org/sqlite"
)

const projectionMarker = "mnemosyne_session_partition_v2"

type Projector struct {
	DataDir string
}

func NewProjector(repoRoot, workspaceID string) (Projector, error) {
	root, err := paths.WorkspaceMemoryRoot(repoRoot, workspaceID)
	if err != nil {
		return Projector{}, err
	}
	return Projector{DataDir: filepath.Join(root, "mnemosyne")}, nil
}

func (Projector) BackendID() string { return "mnemosyne" }

func (Projector) PartitionIdentity(record memory.MemoryRecord) (memory.NativePartitionIdentity, error) {
	id, err := PartitionID(record)
	if err != nil {
		return memory.NativePartitionIdentity{}, err
	}
	membership := record.BlockMembership
	return memory.NativePartitionIdentity{BackendID: "mnemosyne", WorkspaceID: record.WorkspaceID,
		PrimaryBlockID: membership.PrimaryBlockID, CatalogRevisionID: membership.CatalogRevisionID,
		PartitionID: id, IsolationClass: "physical", PartitionMode: "native_session_partition"}, nil
}

func (p Projector) Probe(context.Context) error {
	if p.DataDir == "" {
		return errors.New("Mnemosyne native data directory is required")
	}
	if _, err := os.Stat(filepath.Join(p.DataDir, "mnemosyne.db")); err == nil {
		return errors.New("existing Mnemosyne native projections require partition rebuild")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.MkdirAll(p.DataDir, 0o755)
}

func (Projector) RequiresNative(memory.MemoryRecord) bool { return true }

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func PartitionID(record memory.MemoryRecord) (string, error) {
	if record.BlockMembership == nil || record.BlockMembership.PrimaryBlockID == "" ||
		record.BlockMembership.CatalogRevisionID == "" || record.BlockMembership.BackendPartitionID == "" ||
		record.WorkspaceID == "" {
		return "", errors.New("Mnemosyne native partition requires bound workspace, block, catalog revision and partition")
	}
	return "um-mem:" + digest(record.WorkspaceID)[:16] + ":partition:" +
		digest(record.BlockMembership.BackendPartitionID)[:16] + ":block:" +
		record.BlockMembership.PrimaryBlockID, nil
}

func (p Projector) partitionPath(record memory.MemoryRecord) (string, error) {
	if p.DataDir == "" {
		return "", errors.New("Mnemosyne native data directory is required")
	}
	partition, err := PartitionID(record)
	if err != nil {
		return "", err
	}
	return filepath.Join(p.DataDir, "partitions", digest(partition), "mnemosyne.db"), nil
}

func source(record memory.MemoryRecord) string {
	return "um_mem:block:" + record.BlockMembership.PrimaryBlockID
}

func idempotencyKey(record memory.MemoryRecord, revision int64) string {
	return digest(record.WorkspaceID + "\x00" + record.BlockID + "\x00" +
		strconv.FormatInt(revision, 10) + "\x00" + digest(record.Content))
}

func contentEnvelope(record memory.MemoryRecord, revision int64) string {
	return record.Content + "\n[UMM:" + idempotencyKey(record, revision) + "]"
}

var datePattern = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)
var durationPattern = regexp.MustCompile(`(?i)\b\d+\s(?:days|weeks|months|years)\b`)

func legacyTemporalTags(content string) string {
	if dates := datePattern.FindAllString(content, -1); len(dates) > 0 {
		content += " [DATES: " + strings.Join(dates, ", ") + "]"
	}
	if durations := durationPattern.FindAllString(content, -1); len(durations) > 0 {
		content += " [DURATIONS: " + strings.Join(durations, ", ") + "]"
	}
	return content
}

func projectionMetadata(record memory.MemoryRecord, payload string, revision int64, partition string) (string, error) {
	values := make(map[string]any, len(record.Metadata)+39)
	for key, value := range record.Metadata {
		if key != "importance" {
			values[key] = value
		}
	}
	evidenceID := record.BlockID
	for _, candidate := range []any{record.Metadata["evidence_id"], record.Metadata["source_path"],
		record.ExternalRefs["source_path"], record.ExternalRefs["evidence_id"]} {
		if text, ok := candidate.(string); ok && text != "" {
			evidenceID = text
			break
		}
	}
	for key, value := range map[string]any{
		"block_id": record.BlockID, "commit_revision": revision, "evidence_id": evidenceID,
		"source_identity":   "um-mem:" + record.WorkspaceID + ":" + record.BlockID,
		"primary_block_id":  record.BlockMembership.PrimaryBlockID,
		"projection_marker": projectionMarker, "session_partition_id": partition,
		"catalog_revision_id":      record.BlockMembership.CatalogRevisionID,
		"native_idempotency_key":   idempotencyKey(record, revision),
		"canonical_content_sha256": digest(record.Content), "title": record.Title,
		"kind": record.Kind, "status": record.Status, "retention": record.Retention,
		"delivery_policy": record.DeliveryPolicy, "temperature": record.Temperature,
		"authority": record.Authority, "trust_level": record.TrustLevel, "scope": record.Scope,
		"workspace_id": record.WorkspaceID, "run_id": record.RunID,
		"subtask_id": record.SubtaskID, "producer": record.Producer,
		"tags": record.Tags, "external_refs": record.ExternalRefs,
		"sensitivity": record.Sensitivity, "content_mode": record.ContentMode,
		"operational_risk": record.OperationalRisk, "revision_of": record.RevisionOf,
		"supersedes_ids": record.SupersedesIDs, "verification_status": record.VerificationStatus,
		"verification_methods": record.VerificationMethods,
		"promotion_decision":   record.PromotionDecision, "expires_at": record.ExpiresAt,
		"created_at": record.CreatedAt, "updated_at": record.UpdatedAt,
		"canonical_record_json": payload,
	} {
		values[key] = value
	}
	serialized, err := json.Marshal(values)
	return string(serialized), err
}

type nativeRow struct {
	ID       string
	Content  string
	Source   string
	Session  string
	Metadata string
}

type nativeQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readNativeRows(ctx context.Context, db nativeQuerier, source string) ([]nativeRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,content,source,session_id,metadata_json FROM working_memory WHERE source=?
		UNION ALL SELECT id,content,source,session_id,metadata_json FROM episodic_memory WHERE source=?`, source, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]nativeRow, 0)
	seen := make(map[string]bool)
	for rows.Next() {
		var row nativeRow
		if err := rows.Scan(&row.ID, &row.Content, &row.Source, &row.Session, &row.Metadata); err != nil {
			return nil, err
		}
		if seen[row.ID] {
			// The provider's get_all_memories prefers working memory when the
			// same ID is also represented in the episodic tier.
			continue
		}
		seen[row.ID] = true
		result = append(result, row)
	}
	return result, rows.Err()
}

func verifyNative(row nativeRow, record memory.MemoryRecord, revision int64, partition string) error {
	return verifyNativePayload(row, record, revision, partition, "")
}

// canonicalJSON, when present, must come from the validated ProjectionRow for
// this exact record. Byte equality avoids decoding the same canonical object
// twice during recall. Different encodings and topology overlays still take
// the full semantic verification path; no hash or cross-request cache is used.
func verifyNativePayload(row nativeRow, record memory.MemoryRecord, revision int64, partition, canonicalJSON string) error {
	if row.ID == "" || row.Source != source(record) || row.Session != partition {
		return errors.New("Mnemosyne native row escaped its session partition")
	}
	decoder := json.NewDecoder(strings.NewReader(row.Metadata))
	decoder.UseNumber()
	var metadata map[string]any
	if err := decoder.Decode(&metadata); err != nil || metadata == nil {
		return errors.New("Mnemosyne native metadata is invalid")
	}
	for key, expected := range map[string]string{
		"block_id": record.BlockID, "workspace_id": record.WorkspaceID,
		"source_identity":   "um-mem:" + record.WorkspaceID + ":" + record.BlockID,
		"primary_block_id":  record.BlockMembership.PrimaryBlockID,
		"projection_marker": projectionMarker, "session_partition_id": partition,
		"catalog_revision_id":      record.BlockMembership.CatalogRevisionID,
		"native_idempotency_key":   idempotencyKey(record, revision),
		"canonical_content_sha256": digest(record.Content),
	} {
		if metadata[key] != expected {
			return fmt.Errorf("Mnemosyne native %s differs from canonical record", key)
		}
	}
	if metadata["commit_revision"] != json.Number(strconv.FormatInt(revision, 10)) {
		return errors.New("Mnemosyne native commit revision differs from canonical journal")
	}
	serialized, ok := metadata["canonical_record_json"].(string)
	if !ok || serialized == "" {
		return errors.New("Mnemosyne native canonical record is missing")
	}
	if canonicalJSON == "" || serialized != canonicalJSON {
		decoded, err := memory.DecodeCanonicalRecord([]byte(serialized))
		if err != nil || !reflect.DeepEqual(decoded, record) {
			return errors.New("Mnemosyne native canonical record differs from canonical journal")
		}
	}
	envelope := contentEnvelope(record, revision)
	if row.Content != envelope && row.Content != legacyTemporalTags(envelope) {
		return errors.New("Mnemosyne native content differs from canonical record")
	}
	return nil
}

func findProjection(ctx context.Context, db nativeQuerier, record memory.MemoryRecord, revision int64, partition string) (string, error) {
	rows, err := readNativeRows(ctx, db, source(record))
	if err != nil {
		return "", err
	}
	matched := ""
	identity := "um-mem:" + record.WorkspaceID + ":" + record.BlockID
	for _, row := range rows {
		var metadata map[string]any
		if err := json.Unmarshal([]byte(row.Metadata), &metadata); err != nil {
			return "", errors.New("Mnemosyne native source metadata is invalid")
		}
		if metadata["source_identity"] != identity && metadata["block_id"] != record.BlockID {
			continue
		}
		if err := verifyNative(row, record, revision, partition); err != nil {
			return "", err
		}
		if matched != "" {
			return "", errors.New("Mnemosyne native source identity is ambiguous")
		}
		matched = row.ID
	}
	return matched, nil
}

// Project checks the BEAM-native partition first, so an interrupted canonical
// commit cannot create a second provider row. It writes both the working and
// legacy Mnemosyne tables and verifies the result before acknowledging.
func (p Projector) Project(ctx context.Context, record memory.MemoryRecord, payload string, revision int64) (store.ProjectionResult, error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		result, err := p.projectOnce(ctx, record, payload, revision)
		if err == nil || (!strings.Contains(err.Error(), "SQLITE_BUSY") &&
			!strings.Contains(err.Error(), "database is locked")) || time.Now().After(deadline) {
			return result, err
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return store.ProjectionResult{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (p Projector) projectOnce(ctx context.Context, record memory.MemoryRecord, payload string, revision int64) (store.ProjectionResult, error) {
	if revision < 1 || record.BlockMembership == nil {
		return store.ProjectionResult{}, errors.New("Mnemosyne projection requires a bound record and commit revision")
	}
	decoded, err := memory.DecodeCanonicalRecord([]byte(payload))
	if err != nil || !reflect.DeepEqual(decoded, record) {
		return store.ProjectionResult{}, errors.New("Mnemosyne projection payload differs from canonical record")
	}
	partition, err := PartitionID(record)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	path, err := p.partitionPath(record)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return store.ProjectionResult{}, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := ensureSchema(ctx, db); err != nil {
		return store.ProjectionResult{}, err
	}
	connection, err := db.Conn(ctx)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return store.ProjectionResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	nativeID, err := findProjection(ctx, connection, record, revision, partition)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	if nativeID != "" {
		if _, err := connection.ExecContext(ctx, `COMMIT`); err != nil {
			return store.ProjectionResult{}, err
		}
		committed = true
		return store.ProjectionResult{NativeID: nativeID, Verified: true}, nil
	}
	metadata, err := projectionMetadata(record, payload, revision, partition)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	randomID := make([]byte, 8)
	if _, err := rand.Read(randomID); err != nil {
		return store.ProjectionResult{}, err
	}
	nativeID = hex.EncodeToString(randomID)
	stamp := time.Now().Format("2006-01-02T15:04:05.999999")
	importance := 0.5
	if record.VerificationStatus == "verified" {
		importance = 0.8
	}
	trustTier := "STATED"
	if strings.Contains(strings.ToUpper(record.TrustLevel), "IMPORTED") {
		trustTier = "IMPORTED"
	}
	_, err = connection.ExecContext(ctx, `INSERT INTO working_memory
		(id,content,source,timestamp,session_id,importance,metadata_json,valid_until,scope,veracity,trust_tier)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, nativeID, contentEnvelope(record, revision), source(record),
		stamp, partition, importance, metadata, nullable(record.ExpiresAt), "session", "unknown", trustTier)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	_, err = connection.ExecContext(ctx, `INSERT INTO memories
		(id,content,source,timestamp,session_id,importance,metadata_json) VALUES (?,?,?,?,?,?,?)`,
		nativeID, contentEnvelope(record, revision), source(record), stamp, partition, importance, metadata)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	if _, err := connection.ExecContext(ctx, `COMMIT`); err != nil {
		return store.ProjectionResult{}, err
	}
	committed = true
	verifiedID, err := findProjection(ctx, connection, record, revision, partition)
	if err != nil || verifiedID != nativeID {
		return store.ProjectionResult{}, errors.Join(err, errors.New("Mnemosyne native write verification failed"))
	}
	return store.ProjectionResult{NativeID: nativeID, Verified: true}, nil
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
