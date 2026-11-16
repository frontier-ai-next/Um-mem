package localservice

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ChangeEvent struct {
	ID                 string   `json:"id"`
	At                 string   `json:"at"`
	Kind               string   `json:"kind"`
	Operation          string   `json:"operation"`
	OK                 bool     `json:"ok"`
	SessionID          string   `json:"session_id,omitempty"`
	TaskID             string   `json:"task_id,omitempty"`
	RecordID           string   `json:"record_id,omitempty"`
	RecordIDs          []string `json:"record_ids,omitempty"`
	Count              int      `json:"count,omitempty"`
	Rendered           bool     `json:"rendered,omitempty"`
	CommitRevision     string   `json:"commit_revision,omitempty"`
	ProjectionRevision string   `json:"projection_revision,omitempty"`
	ReasonCode         string   `json:"reason_code,omitempty"`
}

type ChangeFeed struct {
	mu       sync.Mutex
	epoch    string
	sequence uint64
	capacity int
	events   []ChangeEvent
	updates  chan struct{}
}

func NewChangeFeed(capacity int) (*ChangeFeed, error) {
	if capacity < 1 || capacity > 4096 {
		return nil, errors.New("panel feed capacity is invalid")
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return &ChangeFeed{epoch: hex.EncodeToString(nonce), capacity: capacity,
		events: make([]ChangeEvent, 0, capacity), updates: make(chan struct{})}, nil
}

func (feed *ChangeFeed) cursorLocked() string {
	return feed.epoch + ":" + strconv.FormatUint(feed.sequence, 10)
}

func (feed *ChangeFeed) Cursor() string {
	feed.mu.Lock()
	defer feed.mu.Unlock()
	return feed.cursorLocked()
}

func (feed *ChangeFeed) Publish(event ChangeEvent) ChangeEvent {
	feed.mu.Lock()
	defer feed.mu.Unlock()
	feed.sequence++
	event.ID = feed.cursorLocked()
	event.At = time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00")
	event.RecordIDs = append([]string(nil), event.RecordIDs...)
	if len(feed.events) == feed.capacity {
		copy(feed.events, feed.events[1:])
		feed.events[len(feed.events)-1] = event
	} else {
		feed.events = append(feed.events, event)
	}
	close(feed.updates)
	feed.updates = make(chan struct{})
	return event
}

func (feed *ChangeFeed) Snapshot() []ChangeEvent {
	feed.mu.Lock()
	defer feed.mu.Unlock()
	result := append([]ChangeEvent(nil), feed.events...)
	for i := range result {
		result[i].RecordIDs = append([]string(nil), result[i].RecordIDs...)
	}
	return result
}

func (feed *ChangeFeed) sinceLocked(cursor string) (bool, []ChangeEvent) {
	parts := strings.Split(cursor, ":")
	if len(parts) != 2 || parts[0] != feed.epoch {
		return true, nil
	}
	sequence, err := strconv.ParseUint(parts[1], 10, 64)
	oldest := feed.sequence - uint64(len(feed.events))
	if err != nil || sequence < oldest || sequence > feed.sequence {
		return true, nil
	}
	result := make([]ChangeEvent, 0, feed.sequence-sequence)
	for _, event := range feed.events {
		at := strings.LastIndexByte(event.ID, ':')
		value, err := strconv.ParseUint(event.ID[at+1:], 10, 64)
		if err == nil && value > sequence {
			copyEvent := event
			copyEvent.RecordIDs = append([]string(nil), event.RecordIDs...)
			result = append(result, copyEvent)
		}
	}
	return false, result
}

func (feed *ChangeFeed) Since(cursor string) (bool, []ChangeEvent) {
	feed.mu.Lock()
	defer feed.mu.Unlock()
	return feed.sinceLocked(cursor)
}

// Wait wakes on a new event or context cancellation. The cursor and channel
// are inspected under one lock so a commit between the check and wait is seen.
func (feed *ChangeFeed) Wait(ctx context.Context, cursor string, timeout time.Duration) error {
	feed.mu.Lock()
	if cursor != feed.cursorLocked() {
		feed.mu.Unlock()
		return nil
	}
	updates := feed.updates
	feed.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-updates:
		return nil
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (feed *ChangeFeed) Wake() {
	feed.mu.Lock()
	defer feed.mu.Unlock()
	close(feed.updates)
	feed.updates = make(chan struct{})
}
