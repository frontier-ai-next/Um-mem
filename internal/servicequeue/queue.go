package servicequeue

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS queue_metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS work_items (
 work_id TEXT PRIMARY KEY, lane TEXT NOT NULL, work_ref_sha256 TEXT NOT NULL,
 work_revision TEXT NOT NULL, state TEXT NOT NULL, attempts INTEGER NOT NULL,
 max_attempts INTEGER NOT NULL, submitted_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 error_code TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS work_items_lane_state
 ON work_items(lane,state,submitted_at,work_id);
CREATE TABLE IF NOT EXISTS admission_receipts (
 admission_id TEXT PRIMARY KEY, work_id TEXT NOT NULL, lane TEXT NOT NULL,
 status TEXT NOT NULL, reason TEXT NOT NULL, recorded_at TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS attempt_receipts (
 attempt_id INTEGER PRIMARY KEY AUTOINCREMENT, work_id TEXT NOT NULL,
 lane TEXT NOT NULL, attempt INTEGER NOT NULL, outcome TEXT NOT NULL,
 error_code TEXT NOT NULL, recorded_at TEXT NOT NULL);`

var Lanes = []string{"indexing", "working_continuity", "prospective_scheduler",
	"consolidation_transition", "policy_shadow_qualification"}

type Policy struct {
	Capacity        int `json:"capacity"`
	TargetLatencyMS int `json:"target_latency_ms"`
}

var DefaultPolicies = map[string]Policy{
	"indexing":                    {64, 5000},
	"working_continuity":          {32, 1000},
	"prospective_scheduler":       {64, 1000},
	"consolidation_transition":    {32, 10000},
	"policy_shadow_qualification": {16, 30000},
}

type AdmissionReceipt struct {
	AdmissionID string `json:"admission_id"`
	WorkID      string `json:"work_id"`
	Lane        string `json:"lane"`
	Status      string `json:"status"`
	Reason      string `json:"reason"`
	RecordedAt  string `json:"recorded_at"`
}

type Item struct {
	WorkID        string `json:"work_id"`
	Lane          string `json:"lane"`
	WorkRefSHA256 string `json:"work_ref_sha256"`
	WorkRevision  string `json:"work_revision"`
	State         string `json:"state"`
	Attempts      int    `json:"attempts"`
	MaxAttempts   int    `json:"max_attempts"`
	SubmittedAt   string `json:"submitted_at"`
	UpdatedAt     string `json:"updated_at"`
	ErrorCode     string `json:"error_code"`
}

type LaneHealth struct {
	Lane               string `json:"lane"`
	Capacity           int    `json:"capacity"`
	TargetLatencyMS    int    `json:"target_latency_ms"`
	Pending            int    `json:"pending"`
	Running            int    `json:"running"`
	Completed          int    `json:"completed"`
	Poisoned           int    `json:"poisoned"`
	Backpressured      int    `json:"backpressured"`
	OldestPendingAgeMS int    `json:"oldest_pending_age_ms"`
	SLOBreached        bool   `json:"slo_breached"`
}

type Health struct {
	Lanes             []LaneHealth `json:"lanes"`
	EmergencyReadOnly bool         `json:"emergency_read_only"`
	NoSilentDrop      bool         `json:"no_silent_drop"`
}

type Store struct {
	db       *sql.DB
	path     string
	policies map[string]Policy
}

func validLane(lane string) bool {
	for _, known := range Lanes {
		if lane == known {
			return true
		}
	}
	return false
}

func critical(lane string) bool {
	return lane == "indexing" || lane == "consolidation_transition"
}

func bounded(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len([]rune(value)) > 256 {
		return "", fmt.Errorf("%s must be a bounded non-empty identifier", field)
	}
	return value, nil
}

func hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func timestamp(now time.Time) string {
	return now.UTC().Format("2006-01-02T15:04:05.000000+00:00")
}

func Open(path string, policies map[string]Policy, recoverAbandoned bool) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("service work queue path is required")
	}
	var err error
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if policies == nil {
		policies = DefaultPolicies
	}
	if len(policies) != len(Lanes) {
		return nil, errors.New("queue policies must define every service work lane")
	}
	selected := make(map[string]Policy, len(policies))
	for lane, policy := range policies {
		if !validLane(lane) || policy.Capacity < 1 || policy.TargetLatencyMS < 1 {
			return nil, errors.New("queue policies are invalid")
		}
		selected[lane] = policy
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, path: path, policies: selected}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO queue_metadata(key,value) VALUES('emergency_read_only','0')`); err != nil {
		db.Close()
		return nil, err
	}
	if recoverAbandoned {
		if err := store.recoverAbandoned(context.Background(), time.Now()); err != nil {
			db.Close()
			return nil, err
		}
	}
	return store, nil
}

func (store *Store) Close() error { return store.db.Close() }

func (store *Store) Path() string { return store.path }

// RecoverAbandoned consumes a listener's claimed jobs only after the caller
// has exclusively published its registry. It is safe to call more than once.
func (store *Store) RecoverAbandoned(ctx context.Context) error {
	return store.recoverAbandoned(ctx, time.Now())
}

func (store *Store) beginImmediate(ctx context.Context) (*sql.Conn, error) {
	conn, err := store.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func randomAdmissionID(workID, recordedAt string) (string, error) {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return hash("service-admission:" + workID + ":" + recordedAt + ":" + hex.EncodeToString(nonce)), nil
}

func scanItem(scanner interface{ Scan(...any) error }) (Item, error) {
	var item Item
	err := scanner.Scan(&item.WorkID, &item.Lane, &item.WorkRefSHA256,
		&item.WorkRevision, &item.State, &item.Attempts, &item.MaxAttempts,
		&item.SubmittedAt, &item.UpdatedAt, &item.ErrorCode)
	return item, err
}

const itemColumns = `work_id,lane,work_ref_sha256,work_revision,state,attempts,max_attempts,
 submitted_at,updated_at,error_code`

func (store *Store) itemOn(ctx context.Context, conn *sql.Conn, workID string) (Item, error) {
	item, err := scanItem(conn.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM work_items WHERE work_id=?`, workID))
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, errors.New("unknown service work item")
	}
	return item, err
}
