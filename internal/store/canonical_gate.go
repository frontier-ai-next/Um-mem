package store

import (
	"context"
	"errors"
	"time"

	"github.com/gofrs/flock"
)

// WithCanonicalWriteGate fences the final record-set check and catalog
// publication against canonical writers. It takes the same file-lock path as
// the Python runtime, then reserves SQLite's writer lane. Native projection
// must finish before entering this gate.
func (writer *Writer) WithCanonicalWriteGate(ctx context.Context,
	work func() error) (gateErr error) {
	if writer == nil || writer.db == nil || writer.path == "" || work == nil {
		return errors.New("canonical write gate requires an open writer and operation")
	}
	lock := flock.New(writer.path + ".canonical.lock")
	lockCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 50*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return errors.New("canonical write gate timed out")
	}
	defer func() { gateErr = errors.Join(gateErr, lock.Unlock()) }()
	connection, err := writer.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		_, err := connection.ExecContext(context.Background(), "ROLLBACK")
		gateErr = errors.Join(gateErr, err)
	}()
	return work()
}
