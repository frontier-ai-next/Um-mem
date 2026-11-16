package receipts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	_ "modernc.org/sqlite"
)

const createTable = `CREATE TABLE IF NOT EXISTS remember_receipts (
    identity_sha256 TEXT NOT NULL,
    request_id_sha256 TEXT NOT NULL,
    request_fingerprint_sha256 TEXT NOT NULL,
    status TEXT NOT NULL,
    response_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY(identity_sha256, request_id_sha256)
)`

type Identity struct {
	WorkspaceID     string
	ProfileID       string
	Backend         string
	CallerRole      string
	ProtocolVersion int
}

type State struct {
	Status    string         `json:"status"`
	Response  map[string]any `json:"response"`
	CreatedAt string         `json:"created_at"`
}

func Path(repoRoot string) string {
	return filepath.Join(repoRoot, ".UM-MEM", "memory", "service_receipts.sqlite")
}

func sha256Hex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func exact(value string) bool { return value != "" && value == strings.TrimSpace(value) }

func (identity Identity) hash() (string, error) {
	if !exact(identity.WorkspaceID) || !exact(identity.ProfileID) || !exact(identity.Backend) ||
		!exact(identity.CallerRole) || identity.ProtocolVersion < 1 {
		return "", errors.New("receipt identity requires exact scope and positive protocol version")
	}
	encoded, err := jsoncompat.MarshalPython(map[string]any{
		"workspace_id": identity.WorkspaceID, "profile_id": identity.ProfileID,
		"backend": identity.Backend, "caller_role": identity.CallerRole,
		"protocol_version": identity.ProtocolVersion,
	}, false)
	if err != nil {
		return "", err
	}
	return sha256Hex(encoded), nil
}

func fingerprint(arguments map[string]any) (string, error) {
	if arguments == nil {
		return "", errors.New("receipt arguments require an object")
	}
	encoded, err := jsoncompat.MarshalPython(arguments, false)
	if err != nil {
		return "", err
	}
	return sha256Hex(encoded), nil
}

// Fingerprint exposes the exact request identity used by durable receipts so
// a canonical operation record can bind its recovery to the same request.
func Fingerprint(arguments map[string]any) (string, error) {
	return fingerprint(arguments)
}

func receiptKey(identity Identity, operationID string) (string, string, error) {
	if !exact(operationID) || len(operationID) > 256 {
		return "", "", errors.New("operation_id must be a bounded exact string")
	}
	identityHash, err := identity.hash()
	if err != nil {
		return "", "", err
	}
	return identityHash, sha256Hex([]byte(operationID)), nil
}

func receiptURI(path string, readOnly bool) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	slash := filepath.ToSlash(absolute)
	if filepath.VolumeName(absolute) != "" {
		slash = "/" + slash
	}
	uri := url.URL{Scheme: "file", Path: slash}
	if readOnly {
		uri.RawQuery = "mode=ro"
	}
	return uri.String(), nil
}

func open(ctx context.Context, path string, readOnly bool) (*sql.DB, error) {
	if readOnly {
		if _, err := os.Stat(path); err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	uri, err := receiptURI(path, readOnly)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=10000"); err != nil {
		db.Close()
		return nil, err
	}
	if !readOnly {
		if _, err := db.ExecContext(ctx, createTable); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

func timestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00")
}

func transaction(ctx context.Context, db *sql.DB, apply func(*sql.Conn) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	if err := apply(conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func decode(raw string) (map[string]any, error) {
	var response map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil || response == nil {
		return nil, errors.New("local service remember receipt response invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("local service remember receipt response invalid")
	}
	if err := validateResponse(response, ""); err != nil {
		return nil, err
	}
	return response, nil
}

// Claim reserves one operation identity. A pending prior claim is returned for
// deterministic retry, and a committed one includes its original response.
func Claim(ctx context.Context, repoRoot string, identity Identity, operationID string, arguments map[string]any) (State, error) {
	identityHash, requestHash, err := receiptKey(identity, operationID)
	if err != nil {
		return State{}, err
	}
	requestFingerprint, err := fingerprint(arguments)
	if err != nil {
		return State{}, err
	}
	db, err := open(ctx, Path(repoRoot), false)
	if err != nil {
		return State{}, err
	}
	defer db.Close()
	now := timestamp()
	var result State
	err = transaction(ctx, db, func(conn *sql.Conn) error {
		var storedFingerprint, status, raw, createdAt string
		err := conn.QueryRowContext(ctx, `SELECT request_fingerprint_sha256,status,response_json,created_at
			FROM remember_receipts WHERE identity_sha256=? AND request_id_sha256=?`,
			identityHash, requestHash).Scan(&storedFingerprint, &status, &raw, &createdAt)
		if err == sql.ErrNoRows {
			if _, err := conn.ExecContext(ctx, `INSERT INTO remember_receipts(
				identity_sha256,request_id_sha256,request_fingerprint_sha256,status,
				response_json,created_at,updated_at) VALUES(?,?,?,'pending','',?,?)`,
				identityHash, requestHash, requestFingerprint, now, now); err != nil {
				return err
			}
			result = State{Status: "new", CreatedAt: now}
			return nil
		}
		if err != nil {
			return err
		}
		if storedFingerprint != requestFingerprint {
			return errors.New("local service remember receipt conflict")
		}
		result.CreatedAt = createdAt
		switch status {
		case "committed":
			response, err := decode(raw)
			if err != nil {
				return err
			}
			result.Status, result.Response = "committed", response
		case "failed":
			response, err := decode(raw)
			if err != nil || response["ok"] != false {
				return errors.New("local service failed receipt response invalid")
			}
			if _, err := conn.ExecContext(ctx, `UPDATE remember_receipts SET status='pending',updated_at=?
				WHERE identity_sha256=? AND request_id_sha256=? AND status='failed'`,
				now, identityHash, requestHash); err != nil {
				return err
			}
			result.Status = "pending"
		case "pending":
			result.Status = "pending"
		default:
			return errors.New("local service remember receipt state invalid")
		}
		return nil
	})
	return result, err
}

// Complete stores an immutable response for a claimed operation. Repeating an
// identical completion succeeds, while a changed response or request fails.
func Complete(ctx context.Context, repoRoot string, identity Identity, operationID string,
	arguments map[string]any, response map[string]any, outcome string) error {
	if outcome != "committed" && outcome != "failed" {
		return errors.New("local service receipt outcome invalid")
	}
	identityHash, requestHash, err := receiptKey(identity, operationID)
	if err != nil {
		return err
	}
	requestFingerprint, err := fingerprint(arguments)
	if err != nil {
		return err
	}
	encoded, err := jsoncompat.MarshalPython(response, false)
	if err != nil {
		return err
	}
	normalized, err := decode(string(encoded))
	if err != nil {
		return err
	}
	if err := validateResponse(normalized, outcome); err != nil {
		return err
	}
	db, err := open(ctx, Path(repoRoot), false)
	if err != nil {
		return err
	}
	defer db.Close()
	return transaction(ctx, db, func(conn *sql.Conn) error {
		var storedFingerprint, status, raw string
		if err := conn.QueryRowContext(ctx, `SELECT request_fingerprint_sha256,status,response_json
			FROM remember_receipts WHERE identity_sha256=? AND request_id_sha256=?`,
			identityHash, requestHash).Scan(&storedFingerprint, &status, &raw); err != nil {
			if err == sql.ErrNoRows {
				return errors.New("local service remember receipt missing")
			}
			return err
		}
		if storedFingerprint != requestFingerprint {
			return errors.New("local service remember receipt conflict")
		}
		if status == "committed" {
			stored, err := decode(raw)
			if err != nil || outcome != "committed" || !responseEqual(stored, response) {
				return errors.New("local service remember receipt response mismatch")
			}
			return nil
		}
		if status != "pending" {
			return errors.New("local service remember receipt state invalid")
		}
		_, err := conn.ExecContext(ctx, `UPDATE remember_receipts SET status=?,response_json=?,updated_at=?
			WHERE identity_sha256=? AND request_id_sha256=?`,
			outcome, string(encoded), timestamp(), identityHash, requestHash)
		return err
	})
}

func validateResponse(response map[string]any, outcome string) error {
	if response == nil {
		return errors.New("local service remember receipt response invalid")
	}
	if hasExactKeys(response, "ok", "error_type", "reason_code", "error_sha256") {
		checksum, ok := response["error_sha256"].(string)
		if response["ok"] != false || !stringField(response, "error_type") ||
			!stringField(response, "reason_code") || !ok || len(checksum) != 64 {
			return errors.New("local service failed receipt response invalid")
		}
		return nil
	}
	if outcome == "failed" {
		return errors.New("local service failed receipt response invalid")
	}
	if hasExactKeys(response, "receipt_kind", "mutation_tool", "response") {
		inner, ok := response["response"].(map[string]any)
		tools := map[string]bool{"remember": true, "observe": true, "amend": true, "promote": true, "create_block": true,
			"amend_block": true, "move_block": true, "archive_block": true,
			"revive_block": true, "delete_block": true, "split_block": true,
			"merge_blocks": true, "add_block_edge": true, "remove_block_edge": true,
			"set_active_mask": true, "amend_active_mask": true,
			"transition_active_mask": true, "observe_maintenance": true,
			"consolidate_observations": true}
		tool, toolOK := response["mutation_tool"].(string)
		if response["receipt_kind"] == "mutation_response" && toolOK && tools[tool] && ok && inner["ok"] == true {
			return nil
		}
		return errors.New("local service mutation receipt response invalid")
	}
	if hasExactKeys(response, "ok", "saved", "canonical_reference", "canonical_key",
		"revision_id", "recorded_at", "native_reference", "backend", "workspace_id") {
		if response["ok"] != true || response["saved"] != true {
			return errors.New("local service remember receipt response invalid")
		}
		for _, field := range []string{"canonical_reference", "canonical_key", "revision_id", "recorded_at", "backend", "workspace_id"} {
			if !stringField(response, field) {
				return errors.New("local service remember receipt response invalid")
			}
		}
		if value := response["native_reference"]; value != nil {
			if _, ok := value.(string); !ok {
				return errors.New("local service remember receipt response invalid")
			}
		}
		return nil
	}
	if hasFields(response, "ok", "saved", "witness_id", "memory_id", "requested_block_id",
		"actual_block_id", "route_outcome", "reason", "status", "authority",
		"verification_status", "evidence_refs", "backend", "workspace_id") {
		allowed := map[string]bool{"ok": true, "saved": true, "witness_id": true, "memory_id": true,
			"requested_block_id": true, "actual_block_id": true, "route_outcome": true,
			"reason": true, "status": true, "authority": true, "verification_status": true,
			"evidence_refs": true, "backend": true, "workspace_id": true,
			"source_id": true, "source_sha256": true, "source_bytes": true,
			"verification_report": true}
		if !onlyKeys(response, allowed) || response["ok"] != true || response["saved"] != true {
			return errors.New("local service observe receipt response invalid")
		}
		for _, field := range []string{"witness_id", "memory_id", "requested_block_id", "actual_block_id",
			"route_outcome", "reason", "status", "authority", "verification_status", "backend", "workspace_id"} {
			if !stringField(response, field) {
				return errors.New("local service observe receipt response invalid")
			}
		}
		if response["witness_id"] == "" || response["route_outcome"] == "" || response["reason"] == "" {
			return errors.New("local service observe receipt response invalid")
		}
		if _, ok := response["evidence_refs"].([]any); !ok {
			return errors.New("local service observe receipt response invalid")
		}
		_, sourceID := response["source_id"]
		_, sourceHash := response["source_sha256"]
		_, sourceBytes := response["source_bytes"]
		if sourceID != sourceHash || sourceID != sourceBytes {
			return errors.New("local service observe source receipt invalid")
		}
		if sourceID {
			checksum, ok := response["source_sha256"].(string)
			if !stringField(response, "source_id") || response["source_id"] == "" ||
				!ok || len(checksum) != 64 || !positiveInteger(response["source_bytes"]) {
				return errors.New("local service observe source receipt invalid")
			}
			for _, character := range checksum {
				if !strings.ContainsRune("0123456789abcdef", character) {
					return errors.New("local service observe source receipt invalid")
				}
			}
		}
		if raw, present := response["verification_report"]; present {
			report, ok := raw.(map[string]any)
			if !ok || !hasFields(report, "report_id", "report_hash", "workspace_hash", "diff_hash",
				"produced_after_event_id", "verifier_id", "passed", "ledger_hash") {
				return errors.New("local service observe receipt response invalid")
			}
		}
		return nil
	}
	if hasExactKeys(response, "ok", "saved", "mutation_tool", "canonical_reference",
		"previous_revision", "revision", "archived", "backend", "workspace_id") {
		tool, ok := response["mutation_tool"].(string)
		if response["ok"] != true || response["saved"] != true ||
			!ok || !map[string]bool{"amend": true, "create_block": true,
			"amend_block": true, "archive_block": true}[tool] {
			return errors.New("local service mutation receipt response invalid")
		}
		for _, field := range []string{"canonical_reference", "previous_revision", "revision", "backend", "workspace_id"} {
			if !stringField(response, field) {
				return errors.New("local service mutation receipt response invalid")
			}
		}
		if _, ok := response["archived"].(bool); !ok || response["canonical_reference"] == "" ||
			response["revision"] == "" || response["backend"] == "" || response["workspace_id"] == "" {
			return errors.New("local service mutation receipt response invalid")
		}
		return nil
	}
	if hasFields(response, "ok", "saved", "route_rejected", "reason", "backend", "workspace_id") {
		allowed := map[string]bool{"ok": true, "saved": true, "route_rejected": true,
			"reason": true, "backend": true, "workspace_id": true,
			"suggested_route": true, "route_decision": true, "policy_issues": true}
		if !onlyKeys(response, allowed) || response["ok"] != false || response["saved"] != false ||
			response["route_rejected"] != true || !stringField(response, "reason") || response["reason"] == "" ||
			!stringField(response, "backend") || !stringField(response, "workspace_id") {
			return errors.New("local service remember receipt response invalid")
		}
		if route, present := response["route_decision"]; present {
			if _, ok := route.(map[string]any); route != nil && !ok {
				return errors.New("local service remember receipt response invalid")
			}
		}
		if policy, present := response["policy_issues"]; present {
			items, ok := policy.([]any)
			if policy != nil && !ok {
				return errors.New("local service remember receipt response invalid")
			}
			for _, item := range items {
				value, ok := item.(string)
				if !ok || value == "" {
					return errors.New("local service remember receipt response invalid")
				}
			}
		}
		if suggested, present := response["suggested_route"]; present {
			if suggested == nil {
				return nil
			}
			value, ok := suggested.(map[string]any)
			if !ok || !hasExactKeys(value, "action", "requested_block_id") ||
				!stringField(value, "action") || value["action"] == "" ||
				!stringField(value, "requested_block_id") || value["requested_block_id"] == "" {
				return errors.New("local service remember receipt response invalid")
			}
		}
		return nil
	}
	return errors.New("local service remember receipt response invalid")
}

func hasFields(value map[string]any, fields ...string) bool {
	for _, field := range fields {
		if _, exists := value[field]; !exists {
			return false
		}
	}
	return true
}

func hasExactKeys(value map[string]any, fields ...string) bool {
	return len(value) == len(fields) && hasFields(value, fields...)
}

func onlyKeys(value map[string]any, allowed map[string]bool) bool {
	for key := range value {
		if !allowed[key] {
			return false
		}
	}
	return true
}

func stringField(value map[string]any, field string) bool {
	_, ok := value[field].(string)
	return ok
}

func positiveInteger(value any) bool {
	switch number := value.(type) {
	case int:
		return number > 0
	case int64:
		return number > 0
	case json.Number:
		parsed, err := number.Int64()
		return err == nil && parsed > 0
	default:
		return false
	}
}

// Status inspects one existing receipt without creating or claiming it.
func Status(ctx context.Context, repoRoot string, identity Identity, operationID string) (*State, error) {
	identityHash, requestHash, err := receiptKey(identity, operationID)
	if err != nil {
		return nil, err
	}
	db, err := open(ctx, Path(repoRoot), true)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var status, raw, createdAt string
	err = db.QueryRowContext(ctx, `SELECT status,response_json,created_at FROM remember_receipts
		WHERE identity_sha256=? AND request_id_sha256=?`, identityHash, requestHash).Scan(&status, &raw, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := &State{Status: status, CreatedAt: createdAt}
	if status == "pending" {
		return result, nil
	}
	if status != "committed" && status != "failed" {
		return nil, errors.New("local service remember receipt state invalid")
	}
	result.Response, err = decode(raw)
	if err != nil {
		return nil, fmt.Errorf("read receipt: %w", err)
	}
	return result, nil
}

func responseEqual(left, right map[string]any) bool {
	l, err := jsoncompat.MarshalPython(left, false)
	if err != nil {
		return false
	}
	r, err := jsoncompat.MarshalPython(right, false)
	if err != nil {
		return false
	}
	return bytes.Equal(l, r)
}
