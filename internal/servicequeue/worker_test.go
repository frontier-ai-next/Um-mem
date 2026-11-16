package servicequeue

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

type workError string

func (err workError) Error() string { return "private backend detail" }
func (err workError) Code() string  { return string(err) }

func workerHandlers(handler Handler) map[string]Handler {
	handlers := make(map[string]Handler, len(Lanes))
	for _, lane := range Lanes {
		handlers[lane] = handler
	}
	return handlers
}

func TestWorkerRecordsFailureWithoutSensitiveErrorText(t *testing.T) {
	ctx := context.Background()
	queue, err := Open(filepath.Join(t.TempDir(), "queue.sqlite"), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	worker, err := NewWorker(queue, workerHandlers(func(context.Context, Item) error {
		return workError("native_unavailable")
	}))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := queue.Submit(ctx, "working_continuity", "session", "revision", 1)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := worker.ProcessOne(ctx, "working_continuity")
	if err != nil || resolved == nil || resolved.State != "poisoned" ||
		resolved.ErrorCode != "native_unavailable" {
		t.Fatalf("worker failure receipt: %#v, %v", resolved, err)
	}
	if resolved.ErrorCode == "private backend detail" {
		t.Fatal("handler error text persisted")
	}
	stored, err := queue.Item(ctx, receipt.WorkID)
	if err != nil || stored.Attempts != 1 {
		t.Fatalf("attempt not persisted: %#v, %v", stored, err)
	}
	if idle, err := worker.ProcessOne(ctx, "working_continuity"); err != nil || idle != nil {
		t.Fatalf("poisoned work retried automatically: %#v, %v", idle, err)
	}
}

func TestWorkerRunsEachLaneAndStops(t *testing.T) {
	ctx := context.Background()
	queue, err := Open(filepath.Join(t.TempDir(), "queue.sqlite"), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	worker, err := NewWorker(queue, workerHandlers(func(context.Context, Item) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	for _, lane := range Lanes {
		if _, err := queue.Submit(ctx, lane, "record-"+lane, "revision-1", 3); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		health, err := queue.Health(ctx, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		complete := true
		for _, lane := range health.Lanes {
			complete = complete && lane.Completed == 1
		}
		if complete {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	closeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := worker.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	health, err := queue.Health(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, lane := range health.Lanes {
		if lane.Completed != 1 {
			t.Fatalf("lane %s was not processed: %#v", lane.Lane, lane)
		}
	}
	if err := worker.Start(ctx); err == nil {
		t.Fatal("worker started twice")
	}
}

func TestWorkerRequiresEveryLane(t *testing.T) {
	queue, err := Open(filepath.Join(t.TempDir(), "queue.sqlite"), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if _, err := NewWorker(queue, map[string]Handler{"indexing": nil}); err == nil {
		t.Fatal("incomplete worker handlers were accepted")
	}
}

func TestWorkerErrorCodeDoesNotPersistContent(t *testing.T) {
	if code := handlerErrorCode(workError("private token: abc123")); code != "handler_failed" {
		t.Fatalf("handler code allowed free text: %q", code)
	}
}
