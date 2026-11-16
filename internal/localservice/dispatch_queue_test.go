package localservice

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestDispatchQueueBoundsConcurrentAndWaitingCalls(t *testing.T) {
	server, err := New(Identity{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", Backend: "mnemosyne"}, nil,
		func(context.Context, Caller, string, map[string]any) map[string]any {
			return map[string]any{"ok": true}
		})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var running sync.WaitGroup
	for range 4 {
		running.Add(1)
		go func() {
			defer running.Done()
			server.dispatchBounded(ctx, func() map[string]any {
				entered <- struct{}{}
				<-release
				return map[string]any{"ok": true}
			})
		}()
	}
	for range 4 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			cancel()
			close(release)
			running.Wait()
			t.Fatal("four dispatcher slots did not become active")
		}
	}
	var waiting sync.WaitGroup
	for range 32 {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			server.dispatchBounded(ctx, func() map[string]any { return map[string]any{"ok": true} })
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		server.mu.Lock()
		depth := server.waitingCalls
		server.mu.Unlock()
		if depth == 32 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			close(release)
			waiting.Wait()
			running.Wait()
			t.Fatalf("bounded queue depth reached %d instead of 32", depth)
		}
		time.Sleep(time.Millisecond)
	}
	if full := server.dispatchBounded(ctx, func() map[string]any {
		t.Fatal("overflow call entered dispatcher")
		return nil
	}); full["ok"] != false || full["error"] != "local service dispatch queue is full" {
		t.Fatalf("overflow was not reported: %+v", full)
	}
	cancel()
	waiting.Wait()
	close(release)
	running.Wait()
	server.mu.Lock()
	depth, successes, failures := server.waitingCalls, server.successfulCalls, server.failedCalls
	server.mu.Unlock()
	if depth != 0 || successes != 4 || failures != 33 {
		t.Fatalf("dispatcher lost call accounting: depth=%d success=%d failed=%d", depth, successes, failures)
	}
}
