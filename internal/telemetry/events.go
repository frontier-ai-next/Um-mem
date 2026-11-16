package telemetry

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/gofrs/flock"
)

const eventRotateBytes = 8 * 1024 * 1024

type Event struct {
	EventType    string
	WorkspaceID  string
	RunID        string
	StageID      string
	Backend      string
	Status       string
	Error        string
	Data         map[string]any
	Operation    string
	Decision     string
	Outcome      string
	Reason       string
	SelectedIDs  []string
	ProfileID    string
	UserID       string
	ChatID       string
	DurationMS   *float64
	Component    string
	ParentSpanID string
	Parallelism  map[string]any
	Resources    map[string]any
}

func Path(repoRoot string) string {
	return filepath.Join(repoRoot, ".UM-MEM", "memory", "telemetry", "memory_events.jsonl")
}

func eventHash(payload map[string]any) (string, error) {
	encoded, err := jsoncompat.MarshalPython(payload, false)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func Build(event Event) (map[string]any, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(event.SelectedIDs))
	for _, id := range event.SelectedIDs {
		if id != "" {
			digest := sha256.Sum256([]byte(id))
			ids = append(ids, hex.EncodeToString(digest[:])[:16])
		}
	}
	data := event.Data
	if data == nil {
		data = map[string]any{}
	}
	payload := map[string]any{"schema_version": 1,
		"event_id":    hex.EncodeToString(random[:]),
		"recorded_at": time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00"),
		"event_type":  event.EventType, "workspace_id": event.WorkspaceID,
		"run_id": event.RunID, "stage_id": event.StageID, "backend": event.Backend,
		"status": event.Status, "error": event.Error, "data": data,
		"turn_index": nil, "operation": event.Operation, "decision": event.Decision,
		"outcome": event.Outcome, "reason": event.Reason,
		"candidate_count": nil, "selected_id_hashes": ids}
	for key, value := range map[string]string{"profile_id": event.ProfileID,
		"user_id": event.UserID, "chat_id": event.ChatID,
		"component": event.Component, "parent_span_id": event.ParentSpanID} {
		if value != "" {
			payload[key] = value
		}
	}
	if event.DurationMS != nil {
		payload["duration_ms"] = *event.DurationMS
	}
	if len(event.Parallelism) > 0 {
		payload["parallelism"] = event.Parallelism
	}
	if len(event.Resources) > 0 {
		payload["resources"] = event.Resources
	}
	hash, err := eventHash(payload)
	if err != nil {
		return nil, err
	}
	payload["event_sha256"] = hash
	return payload, nil
}

func rotate(path string) error {
	oldest := path + ".3"
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return err
	}
	for suffix := 2; suffix >= 1; suffix-- {
		source := path + "." + strconv.Itoa(suffix)
		target := path + "." + strconv.Itoa(suffix+1)
		if err := os.Rename(source, target); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(path, path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Record appends one Python-compatible, content-free event and fsyncs it.
func Record(repoRoot string, event Event) error {
	payload, err := Build(event)
	if err != nil {
		return err
	}
	encoded, err := jsoncompat.MarshalPythonDefault(payload, false)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	path := Path(repoRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lock := flock.New(path + ".lock")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return context.DeadlineExceeded
	}
	defer lock.Unlock()
	if info, err := os.Stat(path); err == nil {
		if info.Size()+int64(len(encoded)) > eventRotateBytes {
			if err := rotate(path); err != nil {
				return err
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(encoded)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
