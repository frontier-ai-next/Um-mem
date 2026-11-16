package servicequeue

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testPolicies() map[string]Policy {
	policies := make(map[string]Policy, len(DefaultPolicies))
	for lane, policy := range DefaultPolicies {
		policies[lane] = policy
	}
	policies["indexing"] = Policy{Capacity: 1, TargetLatencyMS: 1}
	return policies
}

func TestTwoQueueConnectionsDoNotClaimTheSameWork(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.sqlite")
	first, err := Open(path, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for i := 0; i < 20; i++ {
		if _, err := first.Submit(ctx, "prospective_scheduler", "event-"+string(rune('a'+i)),
			"revision", 3); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	claimed := map[string]bool{}
	errors := make(chan error, 2)
	var workers sync.WaitGroup
	for _, queue := range []*Store{first, second} {
		workers.Add(1)
		go func(queue *Store) {
			defer workers.Done()
			for {
				item, err := queue.Claim(ctx, "prospective_scheduler")
				if err != nil {
					errors <- err
					return
				}
				if item == nil {
					return
				}
				mu.Lock()
				if claimed[item.WorkID] {
					mu.Unlock()
					errors <- fmt.Errorf("duplicate claim: %s", item.WorkID)
					return
				}
				claimed[item.WorkID] = true
				mu.Unlock()
				if _, err := queue.Complete(ctx, item.WorkID); err != nil {
					errors <- err
					return
				}
			}
		}(queue)
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	if len(claimed) != 20 {
		t.Fatalf("claimed %d of 20 work items", len(claimed))
	}
}

func TestPythonQueueFixtureIsReadableAndReplayable(t *testing.T) {
	fixture, err := os.Open(filepath.Join("testdata", "python_queue.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	path := filepath.Join(t.TempDir(), "queue.sqlite")
	copy, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(copy, fixture); err != nil {
		copy.Close()
		t.Fatal(err)
	}
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	queue, err := Open(path, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	ctx := context.Background()
	const pendingID = "374aec33acecd44bedbcf467d5b598be5637cbb924e6019d229b7f77f04e00dc"
	const poisonedID = "5be3e66a66b3d108747c2021ff75350ce74b4f96e1e87b507171bead7bf44455"
	pending, err := queue.Item(ctx, pendingID)
	if err != nil || pending.State != "pending" || pending.Lane != "indexing" {
		t.Fatalf("Python pending work: %#v, %v", pending, err)
	}
	poisoned, err := queue.Item(ctx, poisonedID)
	if err != nil || poisoned.State != "poisoned" || poisoned.ErrorCode != "native_unavailable" {
		t.Fatalf("Python poisoned work: %#v, %v", poisoned, err)
	}
	replay, err := queue.Submit(ctx, "indexing", "python-record", "revision-7", 3)
	if err != nil || replay.Status != "replayed" || replay.WorkID != pendingID {
		t.Fatalf("Python work ID replay: %#v, %v", replay, err)
	}
}

func TestDurableQueueReplayBackpressureAndRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.sqlite")
	queue, err := Open(path, testPolicies(), false)
	if err != nil {
		t.Fatal(err)
	}
	first, err := queue.Submit(ctx, "indexing", "record-1", "revision-1", 2)
	if err != nil || first.Status != "queued" {
		t.Fatalf("first admission: %#v, %v", first, err)
	}
	replay, err := queue.Submit(ctx, "indexing", "record-1", "revision-1", 2)
	if err != nil || replay.Status != "replayed" || replay.WorkID != first.WorkID ||
		replay.Reason != "pending" {
		t.Fatalf("idempotent replay: %#v, %v", replay, err)
	}
	refused, err := queue.Submit(ctx, "indexing", "record-2", "revision-2", 2)
	if err != nil || refused.Status != "backpressured" || refused.Reason != "lane_capacity_exhausted" {
		t.Fatalf("capacity admission: %#v, %v", refused, err)
	}
	health, err := queue.Health(ctx, time.Now().Add(2*time.Second))
	if err != nil || !health.EmergencyReadOnly || !health.NoSilentDrop ||
		health.Lanes[0].Pending != 1 || health.Lanes[0].Backpressured != 1 ||
		!health.Lanes[0].SLOBreached {
		t.Fatalf("queue health: %#v, %v", health, err)
	}
	claimed, err := queue.Claim(ctx, "indexing")
	if err != nil || claimed == nil || claimed.State != "running" || claimed.WorkID != first.WorkID {
		t.Fatalf("claim: %#v, %v", claimed, err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	queue, err = Open(path, testPolicies(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	recovered, err := queue.Item(ctx, first.WorkID)
	if err != nil || recovered.State != "pending" || recovered.Attempts != 1 ||
		recovered.ErrorCode != "listener_restarted" {
		t.Fatalf("abandoned work recovery: %#v, %v", recovered, err)
	}
	claimed, err = queue.Claim(ctx, "indexing")
	if err != nil || claimed == nil {
		t.Fatalf("recovered claim: %#v, %v", claimed, err)
	}
	completed, err := queue.Complete(ctx, first.WorkID)
	if err != nil || completed.State != "completed" || completed.Attempts != 2 {
		t.Fatalf("completion: %#v, %v", completed, err)
	}
	if _, err := queue.Complete(ctx, first.WorkID); err == nil {
		t.Fatal("completed work was resolved twice")
	}
	health, err = queue.Health(ctx, time.Now())
	if err != nil || health.EmergencyReadOnly || health.Lanes[0].Completed != 1 {
		t.Fatalf("recovered queue health: %#v, %v", health, err)
	}
	count, err := queue.AdmissionCount(ctx, "backpressured")
	if err != nil || count != 1 {
		t.Fatalf("backpressure receipt count: %d, %v", count, err)
	}
}

func TestPoisonRequiresExplicitRequeue(t *testing.T) {
	ctx := context.Background()
	queue, err := Open(filepath.Join(t.TempDir(), "queue.sqlite"), testPolicies(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	receipt, err := queue.Submit(ctx, "indexing", "record-1", "revision-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Claim(ctx, "indexing"); err != nil {
		t.Fatal(err)
	}
	poisoned, err := queue.Fail(ctx, receipt.WorkID, "native_unavailable")
	if err != nil || poisoned.State != "poisoned" || poisoned.Attempts != 1 {
		t.Fatalf("poisoned work: %#v, %v", poisoned, err)
	}
	health, err := queue.Health(ctx, time.Now())
	if err != nil || !health.EmergencyReadOnly || health.Lanes[0].Poisoned != 1 {
		t.Fatalf("poison health: %#v, %v", health, err)
	}
	if claim, err := queue.Claim(ctx, "indexing"); err != nil || claim != nil {
		t.Fatalf("poisoned work was auto-retried: %#v, %v", claim, err)
	}
	requeued, err := queue.RequeuePoison(ctx, receipt.WorkID)
	if err != nil || requeued.State != "pending" || requeued.Attempts != 0 {
		t.Fatalf("explicit requeue: %#v, %v", requeued, err)
	}
	if _, err := queue.RequeuePoison(ctx, receipt.WorkID); err == nil {
		t.Fatal("non-poisoned work was requeued")
	}
}

func TestQueueRejectsInvalidLaneAndIdentifiers(t *testing.T) {
	ctx := context.Background()
	queue, err := Open(filepath.Join(t.TempDir(), "queue.sqlite"), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer queue.Close()
	if _, err := queue.Submit(ctx, "unknown", "record", "revision", 3); err == nil {
		t.Fatal("unknown lane accepted")
	}
	if _, err := queue.Submit(ctx, "indexing", "", "revision", 3); err == nil {
		t.Fatal("empty work reference accepted")
	}
	if _, err := queue.Submit(ctx, "indexing", "record", "revision", 11); err == nil {
		t.Fatal("unbounded retry count accepted")
	}
}
