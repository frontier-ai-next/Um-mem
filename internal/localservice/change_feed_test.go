package localservice

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestChangeFeedReplaysBoundedEventsAndResetsStaleCursor(t *testing.T) {
	feed, err := NewChangeFeed(2)
	if err != nil {
		t.Fatal(err)
	}
	initial := feed.Cursor()
	if !strings.HasSuffix(initial, ":0") {
		t.Fatalf("initial cursor: %q", initial)
	}
	first := feed.Publish(ChangeEvent{Kind: "mutation", Operation: "remember", OK: true,
		RecordIDs: []string{"record-1"}})
	feed.Publish(ChangeEvent{Kind: "retrieval", Operation: "recall", OK: true})
	feed.Publish(ChangeEvent{Kind: "mutation", Operation: "amend", OK: true})
	if reset, _ := feed.Since(initial); !reset {
		t.Fatal("cursor older than the ring was replayed")
	}
	reset, events := feed.Since(first.ID)
	if reset || len(events) != 2 || events[0].Operation != "recall" || events[1].Operation != "amend" {
		t.Fatalf("bounded replay: reset=%v events=%#v", reset, events)
	}
	if reset, _ := feed.Since("other-epoch:3"); !reset {
		t.Fatal("foreign listener cursor was accepted")
	}
	events[0].Operation = "forged"
	if _, again := feed.Since(first.ID); again[0].Operation != "recall" {
		t.Fatal("caller mutated retained feed event")
	}
}

func TestChangeFeedWaitObservesPublishWithoutPolling(t *testing.T) {
	feed, err := NewChangeFeed(256)
	if err != nil {
		t.Fatal(err)
	}
	cursor := feed.Cursor()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- feed.Wait(ctx, cursor, 500*time.Millisecond) }()
	feed.Publish(ChangeEvent{Kind: "mutation", Operation: "remember", OK: true})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("change feed missed a published event")
	}
}
