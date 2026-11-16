package servicequeue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func (store *Store) admission(ctx context.Context, conn *sql.Conn, id, workID, lane, status,
	reason, recordedAt string) (AdmissionReceipt, error) {
	receipt := AdmissionReceipt{id, workID, lane, status, reason, recordedAt}
	_, err := conn.ExecContext(ctx, `INSERT INTO admission_receipts
 (admission_id,work_id,lane,status,reason,recorded_at) VALUES(?,?,?,?,?,?)`,
		receipt.AdmissionID, receipt.WorkID, receipt.Lane, receipt.Status,
		receipt.Reason, receipt.RecordedAt)
	return receipt, err
}

func (store *Store) Submit(ctx context.Context, lane, workRef, workRevision string,
	maxAttempts int) (AdmissionReceipt, error) {
	if !validLane(lane) {
		return AdmissionReceipt{}, errors.New("unknown service work lane")
	}
	reference, err := bounded(workRef, "work_ref")
	if err != nil {
		return AdmissionReceipt{}, err
	}
	revision, err := bounded(workRevision, "work_revision")
	if err != nil {
		return AdmissionReceipt{}, err
	}
	if maxAttempts < 1 || maxAttempts > 10 {
		return AdmissionReceipt{}, errors.New("max_attempts must be between one and ten")
	}
	referenceHash := hash(reference)
	workID := hash("service-work:" + lane + ":" + referenceHash + ":" + revision)
	recordedAt := timestamp(time.Now())
	admissionID, err := randomAdmissionID(workID, recordedAt)
	if err != nil {
		return AdmissionReceipt{}, err
	}
	conn, err := store.beginImmediate(ctx)
	if err != nil {
		return AdmissionReceipt{}, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	var existing string
	err = conn.QueryRowContext(ctx, `SELECT state FROM work_items WHERE work_id=?`, workID).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return AdmissionReceipt{}, err
	}
	status, reason := "queued", "accepted"
	if err == nil {
		status, reason = "replayed", existing
	} else {
		var active int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items
 WHERE lane=? AND state IN ('pending','running')`, lane).Scan(&active); err != nil {
			return AdmissionReceipt{}, err
		}
		if active >= store.policies[lane].Capacity {
			status, reason = "backpressured", "lane_capacity_exhausted"
			if critical(lane) {
				if _, err := conn.ExecContext(ctx, `INSERT OR REPLACE INTO queue_metadata(key,value)
 VALUES('emergency_read_only','1')`); err != nil {
					return AdmissionReceipt{}, err
				}
			}
		} else {
			_, err := conn.ExecContext(ctx, `INSERT INTO work_items
 (work_id,lane,work_ref_sha256,work_revision,state,attempts,max_attempts,
 submitted_at,updated_at,error_code) VALUES(?,?,?,?,?,?,?,?,?,?)`, workID, lane,
				referenceHash, revision, "pending", 0, maxAttempts, recordedAt, recordedAt, "")
			if err != nil {
				return AdmissionReceipt{}, err
			}
		}
	}
	receipt, err := store.admission(ctx, conn, admissionID, workID, lane, status, reason, recordedAt)
	if err != nil {
		return AdmissionReceipt{}, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return AdmissionReceipt{}, err
	}
	return receipt, nil
}

func (store *Store) Item(ctx context.Context, workID string) (Item, error) {
	workID, err := bounded(workID, "work_id")
	if err != nil {
		return Item{}, err
	}
	return scanItem(store.db.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM work_items WHERE work_id=?`, workID))
}

func (store *Store) Claim(ctx context.Context, lane string) (*Item, error) {
	if !validLane(lane) {
		return nil, errors.New("unknown service work lane")
	}
	conn, err := store.beginImmediate(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	var workID string
	err = conn.QueryRowContext(ctx, `SELECT work_id FROM work_items WHERE lane=? AND state='pending'
 ORDER BY submitted_at,work_id LIMIT 1`, lane).Scan(&workID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE work_items SET state='running',updated_at=?
 WHERE work_id=? AND state='pending'`, timestamp(time.Now()), workID); err != nil {
		return nil, err
	}
	item, err := store.itemOn(ctx, conn, workID)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, err
	}
	return &item, nil
}

func (store *Store) Complete(ctx context.Context, workID string) (Item, error) {
	return store.resolve(ctx, workID, "")
}

func (store *Store) Fail(ctx context.Context, workID, errorCode string) (Item, error) {
	code, err := bounded(errorCode, "error_code")
	if err != nil {
		return Item{}, err
	}
	return store.resolve(ctx, workID, code)
}

func (store *Store) resolve(ctx context.Context, workID, errorCode string) (Item, error) {
	workID, err := bounded(workID, "work_id")
	if err != nil {
		return Item{}, err
	}
	conn, err := store.beginImmediate(ctx)
	if err != nil {
		return Item{}, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	item, err := store.itemOn(ctx, conn, workID)
	if err != nil {
		return Item{}, err
	}
	if item.State != "running" {
		return Item{}, errors.New("only running work may be resolved")
	}
	attempt := item.Attempts + 1
	state := "completed"
	if errorCode != "" {
		state = "pending"
		if attempt >= item.MaxAttempts {
			state = "poisoned"
		}
	}
	updatedAt := timestamp(time.Now())
	if _, err := conn.ExecContext(ctx, `UPDATE work_items SET state=?,attempts=?,updated_at=?,error_code=?
 WHERE work_id=?`, state, attempt, updatedAt, errorCode, workID); err != nil {
		return Item{}, err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO attempt_receipts
 (work_id,lane,attempt,outcome,error_code,recorded_at) VALUES(?,?,?,?,?,?)`,
		workID, item.Lane, attempt, state, errorCode, updatedAt); err != nil {
		return Item{}, err
	}
	if err := store.refreshReadOnly(ctx, conn); err != nil {
		return Item{}, err
	}
	item, err = store.itemOn(ctx, conn, workID)
	if err != nil {
		return Item{}, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return Item{}, err
	}
	return item, nil
}

func (store *Store) RequeuePoison(ctx context.Context, workID string) (Item, error) {
	workID, err := bounded(workID, "work_id")
	if err != nil {
		return Item{}, err
	}
	conn, err := store.beginImmediate(ctx)
	if err != nil {
		return Item{}, err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	item, err := store.itemOn(ctx, conn, workID)
	if err != nil {
		return Item{}, err
	}
	if item.State != "poisoned" {
		return Item{}, errors.New("only poisoned work may be requeued")
	}
	var active int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items
 WHERE lane=? AND state IN ('pending','running')`, item.Lane).Scan(&active); err != nil {
		return Item{}, err
	}
	if active >= store.policies[item.Lane].Capacity {
		return Item{}, errors.New("work lane remains backpressured")
	}
	if _, err := conn.ExecContext(ctx, `UPDATE work_items SET state='pending',attempts=0,
 error_code='',updated_at=? WHERE work_id=?`, timestamp(time.Now()), workID); err != nil {
		return Item{}, err
	}
	if err := store.refreshReadOnly(ctx, conn); err != nil {
		return Item{}, err
	}
	item, err = store.itemOn(ctx, conn, workID)
	if err != nil {
		return Item{}, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return Item{}, err
	}
	return item, nil
}

func (store *Store) AdmissionCount(ctx context.Context, status string) (int, error) {
	status, err := bounded(status, "status")
	if err != nil {
		return 0, err
	}
	var count int
	err = store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admission_receipts WHERE status=?`, status).Scan(&count)
	return count, err
}

func (store *Store) refreshReadOnly(ctx context.Context, conn *sql.Conn) error {
	var poisoned, indexingActive, consolidationActive int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items
 WHERE lane IN ('indexing','consolidation_transition') AND state='poisoned'`).Scan(&poisoned); err != nil {
		return err
	}
	for lane, target := range map[string]*int{"indexing": &indexingActive,
		"consolidation_transition": &consolidationActive} {
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM work_items
 WHERE lane=? AND state IN ('pending','running')`, lane).Scan(target); err != nil {
			return err
		}
	}
	readOnly := "0"
	if poisoned > 0 || indexingActive >= store.policies["indexing"].Capacity ||
		consolidationActive >= store.policies["consolidation_transition"].Capacity {
		readOnly = "1"
	}
	_, err := conn.ExecContext(ctx, `INSERT OR REPLACE INTO queue_metadata(key,value)
 VALUES('emergency_read_only',?)`, readOnly)
	return err
}

func (store *Store) recoverAbandoned(ctx context.Context, now time.Time) error {
	conn, err := store.beginImmediate(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	rows, err := conn.QueryContext(ctx, `SELECT work_id,lane,attempts,max_attempts FROM work_items
 WHERE state='running'`)
	if err != nil {
		return err
	}
	type abandoned struct {
		id, lane              string
		attempts, maxAttempts int
	}
	items := []abandoned{}
	for rows.Next() {
		var item abandoned
		if err := rows.Scan(&item.id, &item.lane, &item.attempts, &item.maxAttempts); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	recoveredAt := timestamp(now)
	for _, item := range items {
		attempt := item.attempts + 1
		state, outcome := "pending", "requeued"
		if attempt >= item.maxAttempts {
			state, outcome = "poisoned", "poisoned"
		}
		if _, err := conn.ExecContext(ctx, `UPDATE work_items SET state=?,attempts=?,
 updated_at=?,error_code='listener_restarted' WHERE work_id=?`, state, attempt, recoveredAt, item.id); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO attempt_receipts
 (work_id,lane,attempt,outcome,error_code,recorded_at) VALUES(?,?,?,?,?,?)`,
			item.id, item.lane, attempt, outcome, "listener_restarted", recoveredAt); err != nil {
			return err
		}
	}
	if err := store.refreshReadOnly(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("service queue recovery commit: %w", err)
	}
	return nil
}
