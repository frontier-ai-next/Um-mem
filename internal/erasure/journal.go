package erasure

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var ErrRequestConflict = errors.New("erase request ID has a different fingerprint")
var ErrRequestUnavailable = errors.New("erase request state is unavailable")

type RequestState struct {
	WorkspaceIDSHA256 string `json:"workspace_id_sha256"`
	RequestIDSHA256   string `json:"request_id_sha256"`
	FingerprintSHA256 string `json:"fingerprint_sha256"`
	TargetIDSHA256    string `json:"target_id_sha256"`
	Backend           string `json:"backend"`
	Status            string `json:"status"`
	Error             string `json:"error"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

type Journal struct{ db *sql.DB }

func JournalPath(repoRoot string) string {
	return filepath.Join(repoRoot, ".UM-MEM", "memory", "erase_requests.sqlite")
}

func OpenJournal(repoRoot string) (*Journal, error) {
	path := JournalPath(repoRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS erase_requests (
		workspace_id_sha256 TEXT NOT NULL,
		request_id_sha256 TEXT NOT NULL,
		fingerprint_sha256 TEXT NOT NULL,
		target_id_sha256 TEXT NOT NULL,
		backend TEXT NOT NULL,
		status TEXT NOT NULL,
		error TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(workspace_id_sha256, request_id_sha256)
	)`); err != nil {
		db.Close()
		return nil, err
	}
	return &Journal{db: db}, nil
}

func (journal *Journal) Close() error { return journal.db.Close() }

func eraseNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00") }

func readState(ctx context.Context, conn *sql.Conn, workspaceSHA, requestSHA string) (*RequestState, error) {
	state := &RequestState{}
	err := conn.QueryRowContext(ctx, `SELECT workspace_id_sha256,request_id_sha256,
		fingerprint_sha256,target_id_sha256,backend,status,error,created_at,updated_at
		FROM erase_requests WHERE workspace_id_sha256=? AND request_id_sha256=?`,
		workspaceSHA, requestSHA).Scan(&state.WorkspaceIDSHA256, &state.RequestIDSHA256,
		&state.FingerprintSHA256, &state.TargetIDSHA256, &state.Backend,
		&state.Status, &state.Error, &state.CreatedAt, &state.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return state, err
}

func (journal *Journal) OpenRequest(ctx context.Context, workspaceSHA, requestSHA,
	fingerprintSHA, targetSHA, backend string) (RequestState, bool, error) {
	conn, err := journal.db.Conn(ctx)
	if err != nil {
		return RequestState{}, false, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return RequestState{}, false, err
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	current, err := readState(ctx, conn, workspaceSHA, requestSHA)
	if err != nil {
		return RequestState{}, false, err
	}
	if current != nil {
		if current.FingerprintSHA256 != fingerprintSHA {
			return RequestState{}, false, ErrRequestConflict
		}
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return RequestState{}, false, err
		}
		return *current, false, nil
	}
	now := eraseNow()
	state := RequestState{WorkspaceIDSHA256: workspaceSHA, RequestIDSHA256: requestSHA,
		FingerprintSHA256: fingerprintSHA, TargetIDSHA256: targetSHA, Backend: backend,
		Status: "requested", CreatedAt: now, UpdatedAt: now}
	if _, err := conn.ExecContext(ctx, `INSERT INTO erase_requests(
		workspace_id_sha256,request_id_sha256,fingerprint_sha256,target_id_sha256,
		backend,status,error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		state.WorkspaceIDSHA256, state.RequestIDSHA256, state.FingerprintSHA256,
		state.TargetIDSHA256, state.Backend, state.Status, state.Error,
		state.CreatedAt, state.UpdatedAt); err != nil {
		return RequestState{}, false, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return RequestState{}, false, err
	}
	return state, true, nil
}

func (journal *Journal) RecordOutcome(ctx context.Context, state RequestState,
	status, errorCode string) (RequestState, error) {
	now := eraseNow()
	result, err := journal.db.ExecContext(ctx, `UPDATE erase_requests SET status=?,error=?,updated_at=?
		WHERE workspace_id_sha256=? AND request_id_sha256=? AND fingerprint_sha256=?
		AND status=? AND updated_at=? AND status!='completed'`,
		status, errorCode, now, state.WorkspaceIDSHA256, state.RequestIDSHA256,
		state.FingerprintSHA256, state.Status, state.UpdatedAt)
	if err != nil {
		return RequestState{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return RequestState{}, err
	}
	if changed != 1 {
		return RequestState{}, ErrRequestUnavailable
	}
	state.Status, state.Error, state.UpdatedAt = status, errorCode, now
	return state, nil
}

func (journal *Journal) HasCompletedTarget(ctx context.Context, workspaceSHA string,
	targetSHAs []string) (bool, error) {
	for _, targetSHA := range targetSHAs {
		var found int
		err := journal.db.QueryRowContext(ctx, `SELECT 1 FROM erase_requests
			WHERE workspace_id_sha256=? AND target_id_sha256=? AND status='completed' LIMIT 1`,
			workspaceSHA, targetSHA).Scan(&found)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	return false, nil
}
