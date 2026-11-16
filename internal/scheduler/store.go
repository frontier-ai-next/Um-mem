package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS scheduler_metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS host_events (
 identity_sha256 TEXT NOT NULL, event_id_sha256 TEXT NOT NULL,
 event_fingerprint_sha256 TEXT NOT NULL, decision_plan_json TEXT NOT NULL,
 decision_plan_sha256 TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY (identity_sha256, event_id_sha256));
CREATE TABLE IF NOT EXISTS scheduled_jobs (
 job_id TEXT PRIMARY KEY, identity_sha256 TEXT NOT NULL, event_id_sha256 TEXT NOT NULL,
 event_fingerprint_sha256 TEXT NOT NULL, job_kind TEXT NOT NULL, due_at TEXT NOT NULL,
 decision_revision TEXT NOT NULL, reason_code TEXT NOT NULL, status TEXT NOT NULL,
 created_at TEXT NOT NULL, resolved_at TEXT NOT NULL,
 FOREIGN KEY (identity_sha256, event_id_sha256) REFERENCES host_events(identity_sha256, event_id_sha256));
CREATE INDEX IF NOT EXISTS scheduled_jobs_due_idx ON scheduled_jobs(identity_sha256,status,due_at,job_kind);
CREATE TABLE IF NOT EXISTS scheduler_receipts (
 job_id TEXT PRIMARY KEY, identity_sha256 TEXT NOT NULL, event_id_sha256 TEXT NOT NULL,
 event_fingerprint_sha256 TEXT NOT NULL, job_kind TEXT NOT NULL,
 decision_revision TEXT NOT NULL, status TEXT NOT NULL, outcome TEXT NOT NULL,
 resolved_at TEXT NOT NULL, FOREIGN KEY (job_id) REFERENCES scheduled_jobs(job_id));`

type Job struct {
	JobID                  string    `json:"job_id"`
	EventIDSHA256          string    `json:"event_id_sha256"`
	EventFingerprintSHA256 string    `json:"event_fingerprint_sha256"`
	Kind                   string    `json:"kind"`
	DueAt                  time.Time `json:"due_at"`
	DecisionRevision       string    `json:"decision_revision"`
	ReasonCode             string    `json:"reason_code"`
	Status                 string    `json:"status"`
}

type Submission struct {
	EventIDSHA256          string
	EventFingerprintSHA256 string
	Plan                   AdvisoryDecisionPlan
	PlanSHA256             string
	Jobs                   []Job
	Replayed               bool
}

type Receipt struct {
	JobID                    string
	EventIDSHA256            string
	EventFingerprintSHA256   string
	Kind                     string
	DecisionRevision         string
	Status                   string
	Outcome                  string
	ResolvedAt               time.Time
	MatchedRuleSHA256s       []string
	MatchedActionSHA256s     []string
	DeferredTriggerOwnerRefs []string
	Executed                 bool
}

type Store struct {
	db             *sql.DB
	identitySHA256 string
	workspaceID    string
	planner        func(context.Context, HostStateEvent) (AdvisoryDecisionPlan, error)
}

func Open(path, identitySHA256, workspaceID string,
	planner func(context.Context, HostStateEvent) (AdvisoryDecisionPlan, error)) (*Store, error) {
	if !digestPattern.MatchString(identitySHA256) {
		return nil, errors.New("scheduler identity digest is invalid")
	}
	if err := token(workspaceID, "workspace_id"); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errors.New("scheduler path is required")
	}
	if planner == nil {
		planner = func(_ context.Context, event HostStateEvent) (AdvisoryDecisionPlan, error) {
			return DefaultPlan(event)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	result := &Store{db: db, identitySHA256: identitySHA256,
		workspaceID: workspaceID, planner: planner}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	var stored string
	err = db.QueryRow(`SELECT value FROM scheduler_metadata WHERE key='identity_sha256'`).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = db.Exec(`INSERT INTO scheduler_metadata(key,value) VALUES('schema_version','1'),('identity_sha256',?)`, identitySHA256); err != nil {
			db.Close()
			return nil, err
		}
	} else if err != nil || stored != identitySHA256 {
		db.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("scheduler state path identity mismatch")
	}
	return result, nil
}

func (store *Store) Close() error { return store.db.Close() }

func (store *Store) IdentitySHA256() string { return store.identitySHA256 }

func (store *Store) WorkspaceID() string { return store.workspaceID }

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

func scanJob(scanner interface{ Scan(...any) error }) (Job, error) {
	var job Job
	var due string
	err := scanner.Scan(&job.JobID, &job.EventIDSHA256, &job.EventFingerprintSHA256,
		&job.Kind, &due, &job.DecisionRevision, &job.ReasonCode, &job.Status)
	if err != nil {
		return Job{}, err
	}
	job.DueAt, err = time.Parse(time.RFC3339Nano, due)
	return job, err
}

func (store *Store) jobsFor(ctx context.Context, conn *sql.Conn, eventIDHash string) ([]Job, error) {
	rows, err := conn.QueryContext(ctx, `SELECT job_id,event_id_sha256,event_fingerprint_sha256,
	 job_kind,due_at,decision_revision,reason_code,status FROM scheduled_jobs
	 WHERE identity_sha256=? AND event_id_sha256=? ORDER BY due_at,job_kind,job_id`, store.identitySHA256, eventIDHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(jobs) != 2 {
		return nil, errors.New("scheduler event has an incomplete job set")
	}
	return jobs, nil
}

func planFromJSON(raw string) (AdvisoryDecisionPlan, string, error) {
	var record map[string]any
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return AdvisoryDecisionPlan{}, "", err
	}
	if len(record) != 10 {
		return AdvisoryDecisionPlan{}, "", errors.New("scheduler decision plan is invalid")
	}
	stringField := func(name string) (string, error) {
		value, ok := record[name].(string)
		if !ok {
			return "", errors.New("scheduler decision plan is invalid")
		}
		return value, nil
	}
	stringsField := func(name string) ([]string, error) {
		values, ok := record[name].([]any)
		if !ok {
			return nil, errors.New("scheduler decision plan is invalid")
		}
		result := make([]string, len(values))
		for i, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, errors.New("scheduler decision plan is invalid")
			}
			result[i] = text
		}
		return result, nil
	}
	var plan AdvisoryDecisionPlan
	var err error
	if plan.DecisionRevision, err = stringField("decision_revision"); err != nil {
		return plan, "", err
	}
	if plan.ActivationReasonCode, err = stringField("activation_reason_code"); err != nil {
		return plan, "", err
	}
	if plan.ConsolidationReasonCode, err = stringField("consolidation_reason_code"); err != nil {
		return plan, "", err
	}
	if plan.ConsolidationDisposition, err = stringField("consolidation_disposition"); err != nil {
		return plan, "", err
	}
	if plan.MatchedRuleSHA256s, err = stringsField("matched_rule_sha256s"); err != nil {
		return plan, "", err
	}
	if plan.MatchedActionSHA256s, err = stringsField("matched_action_sha256s"); err != nil {
		return plan, "", err
	}
	if plan.DeferredTriggerOwnerRefs, err = stringsField("deferred_trigger_owner_refs"); err != nil {
		return plan, "", err
	}
	activation, err := stringField("activation_due_at")
	if err != nil {
		return plan, "", err
	}
	consolidation, err := stringField("consolidation_due_at")
	if err != nil {
		return plan, "", err
	}
	plan.ActivationDueAt, err = time.Parse(time.RFC3339Nano, activation)
	if err != nil {
		return plan, "", err
	}
	plan.ConsolidationDueAt, err = time.Parse(time.RFC3339Nano, consolidation)
	if err != nil {
		return plan, "", err
	}
	actual, err := plan.Record()
	if err != nil {
		return plan, "", err
	}
	want, err := stringField("plan_sha256")
	if err != nil {
		return plan, "", err
	}
	if actual["plan_sha256"] != want {
		return plan, "", errors.New("scheduler decision plan fingerprint is invalid")
	}
	return plan, want, nil
}

func (store *Store) Submit(ctx context.Context, event HostStateEvent) (Submission, error) {
	event, err := event.Normalized()
	if err != nil {
		return Submission{}, err
	}
	if event.WorkspaceID != store.workspaceID {
		return Submission{}, errors.New("host-state workspace mismatch")
	}
	eventIDHash, err := EventIDHash(event.EventID)
	if err != nil {
		return Submission{}, err
	}
	fingerprint, err := event.Fingerprint()
	if err != nil {
		return Submission{}, err
	}
	conn, err := store.beginImmediate(ctx)
	if err != nil {
		return Submission{}, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	var storedFingerprint, rawPlan string
	err = conn.QueryRowContext(ctx, `SELECT event_fingerprint_sha256,decision_plan_json
	 FROM host_events WHERE identity_sha256=? AND event_id_sha256=?`, store.identitySHA256, eventIDHash).Scan(&storedFingerprint, &rawPlan)
	if err == nil {
		if storedFingerprint != fingerprint {
			return Submission{}, errors.New("scheduler host-state event conflict")
		}
		plan, planSHA, err := planFromJSON(rawPlan)
		if err != nil {
			return Submission{}, err
		}
		jobs, err := store.jobsFor(ctx, conn, eventIDHash)
		if err != nil {
			return Submission{}, err
		}
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return Submission{}, err
		}
		return Submission{EventIDSHA256: eventIDHash, EventFingerprintSHA256: fingerprint,
			Plan: plan, PlanSHA256: planSHA, Jobs: jobs, Replayed: true}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Submission{}, err
	}
	plan, err := store.planner(ctx, event)
	if err != nil {
		return Submission{}, err
	}
	planRecord, err := plan.Record()
	if err != nil {
		return Submission{}, err
	}
	planSHA := planRecord["plan_sha256"].(string)
	encoded, err := jsoncompat.MarshalPython(planRecord, false)
	if err != nil {
		return Submission{}, err
	}
	now := timestamp(time.Now())
	if _, err := conn.ExecContext(ctx, `INSERT INTO host_events(identity_sha256,event_id_sha256,
	 event_fingerprint_sha256,decision_plan_json,decision_plan_sha256,created_at) VALUES(?,?,?,?,?,?)`,
		store.identitySHA256, eventIDHash, fingerprint, string(encoded), planSHA, now); err != nil {
		return Submission{}, err
	}
	jobs := []Job{}
	for _, item := range []struct {
		kind   string
		due    time.Time
		reason string
	}{
		{"activation_opportunity", plan.ActivationDueAt, plan.ActivationReasonCode},
		{"consolidation_assessment", plan.ConsolidationDueAt, plan.ConsolidationReasonCode}} {
		jobID, err := JobID(fingerprint, item.kind)
		if err != nil {
			return Submission{}, err
		}
		job := Job{JobID: jobID, EventIDSHA256: eventIDHash, EventFingerprintSHA256: fingerprint,
			Kind: item.kind, DueAt: item.due, DecisionRevision: plan.DecisionRevision,
			ReasonCode: item.reason, Status: "pending"}
		if _, err := conn.ExecContext(ctx, `INSERT INTO scheduled_jobs(job_id,identity_sha256,event_id_sha256,
		 event_fingerprint_sha256,job_kind,due_at,decision_revision,reason_code,status,created_at,resolved_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`, jobID, store.identitySHA256, eventIDHash, fingerprint,
			item.kind, timestamp(item.due), plan.DecisionRevision, item.reason, "pending", now, ""); err != nil {
			return Submission{}, err
		}
		jobs = append(jobs, job)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return Submission{}, err
	}
	return Submission{EventIDSHA256: eventIDHash, EventFingerprintSHA256: fingerprint,
		Plan: plan, PlanSHA256: planSHA, Jobs: jobs}, nil
}

func (store *Store) RecoverDue(ctx context.Context, now time.Time) ([]Job, error) {
	if now.IsZero() {
		now = time.Now()
	}
	rows, err := store.db.QueryContext(ctx, `SELECT job_id,event_id_sha256,event_fingerprint_sha256,
	 job_kind,due_at,decision_revision,reason_code,status FROM scheduled_jobs
	 WHERE identity_sha256=? AND status='pending' AND due_at<=? ORDER BY due_at,job_kind,job_id`,
		store.identitySHA256, timestamp(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (store *Store) HasPending(ctx context.Context) (bool, error) {
	var marker int
	err := store.db.QueryRowContext(ctx, `SELECT 1 FROM scheduled_jobs WHERE identity_sha256=? AND status='pending' LIMIT 1`, store.identitySHA256).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (store *Store) JobsFor(ctx context.Context, eventIDHash string) ([]Job, error) {
	if !digestPattern.MatchString(eventIDHash) {
		return nil, fmt.Errorf("event ID digest is invalid")
	}
	conn, err := store.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return store.jobsFor(ctx, conn, eventIDHash)
}

func (store *Store) planFor(ctx context.Context, conn *sql.Conn, eventIDHash string) (AdvisoryDecisionPlan, error) {
	var raw string
	err := conn.QueryRowContext(ctx, `SELECT decision_plan_json FROM host_events
	 WHERE identity_sha256=? AND event_id_sha256=?`, store.identitySHA256, eventIDHash).Scan(&raw)
	if err != nil {
		return AdvisoryDecisionPlan{}, err
	}
	plan, _, err := planFromJSON(raw)
	return plan, err
}

func (store *Store) FireDue(ctx context.Context, now time.Time) ([]Receipt, error) {
	if now.IsZero() {
		now = time.Now()
	}
	resolved := timestamp(now)
	conn, err := store.beginImmediate(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	rows, err := conn.QueryContext(ctx, `SELECT job_id,event_id_sha256,event_fingerprint_sha256,
	 job_kind,due_at,decision_revision,reason_code,status FROM scheduled_jobs
	 WHERE identity_sha256=? AND status='pending' AND due_at<=? ORDER BY due_at,job_kind,job_id`,
		store.identitySHA256, resolved)
	if err != nil {
		return nil, err
	}
	jobs := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	receipts := []Receipt{}
	for _, job := range jobs {
		plan, err := store.planFor(ctx, conn, job.EventIDSHA256)
		if err != nil {
			return nil, err
		}
		status, outcome := "recorded", "activation_opportunity_recorded"
		if job.Kind == "consolidation_assessment" {
			outcome = "consolidation_assessment_recorded"
			if plan.ConsolidationDisposition == "defer" {
				status, outcome = "deferred", "consolidation_deferred"
			}
		} else if job.Kind != "activation_opportunity" {
			return nil, errors.New("scheduler job kind is invalid")
		}
		updated, err := conn.ExecContext(ctx, `UPDATE scheduled_jobs SET status=?,resolved_at=?
		 WHERE job_id=? AND status='pending'`, status, resolved, job.JobID)
		if err != nil {
			return nil, err
		}
		count, err := updated.RowsAffected()
		if err != nil || count != 1 {
			return nil, errors.New("scheduler job state changed while resolving")
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO scheduler_receipts(job_id,identity_sha256,
		 event_id_sha256,event_fingerprint_sha256,job_kind,decision_revision,status,outcome,resolved_at)
		 VALUES(?,?,?,?,?,?,?,?,?)`, job.JobID, store.identitySHA256, job.EventIDSHA256,
			job.EventFingerprintSHA256, job.Kind, job.DecisionRevision, status, outcome, resolved); err != nil {
			return nil, err
		}
		receipts = append(receipts, Receipt{JobID: job.JobID, EventIDSHA256: job.EventIDSHA256,
			EventFingerprintSHA256: job.EventFingerprintSHA256, Kind: job.Kind,
			DecisionRevision: job.DecisionRevision, Status: status, Outcome: outcome,
			ResolvedAt: now.UTC().Truncate(time.Microsecond), MatchedRuleSHA256s: plan.MatchedRuleSHA256s,
			MatchedActionSHA256s:     plan.MatchedActionSHA256s,
			DeferredTriggerOwnerRefs: plan.DeferredTriggerOwnerRefs})
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, err
	}
	return receipts, nil
}

func (store *Store) ReceiptFor(ctx context.Context, jobID string) (*Receipt, error) {
	if !digestPattern.MatchString(jobID) {
		return nil, errors.New("scheduler job ID is invalid")
	}
	var receipt Receipt
	var resolved, rawPlan string
	err := store.db.QueryRowContext(ctx, `SELECT receipts.job_id,receipts.event_id_sha256,
	 receipts.event_fingerprint_sha256,receipts.job_kind,receipts.decision_revision,
	 receipts.status,receipts.outcome,receipts.resolved_at,events.decision_plan_json
	 FROM scheduler_receipts AS receipts JOIN host_events AS events
	 ON events.identity_sha256=receipts.identity_sha256 AND events.event_id_sha256=receipts.event_id_sha256
	 WHERE receipts.identity_sha256=? AND receipts.job_id=?`, store.identitySHA256, jobID).Scan(
		&receipt.JobID, &receipt.EventIDSHA256, &receipt.EventFingerprintSHA256,
		&receipt.Kind, &receipt.DecisionRevision, &receipt.Status, &receipt.Outcome, &resolved, &rawPlan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	plan, _, err := planFromJSON(rawPlan)
	if err != nil {
		return nil, err
	}
	receipt.ResolvedAt, err = time.Parse(time.RFC3339Nano, resolved)
	if err != nil {
		return nil, err
	}
	receipt.MatchedRuleSHA256s, receipt.MatchedActionSHA256s = plan.MatchedRuleSHA256s, plan.MatchedActionSHA256s
	receipt.DeferredTriggerOwnerRefs = plan.DeferredTriggerOwnerRefs
	return &receipt, nil
}
