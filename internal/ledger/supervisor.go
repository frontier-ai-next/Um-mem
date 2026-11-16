package ledger

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/paths"
	"github.com/gofrs/flock"
)

var appendMu sync.Mutex

type Event struct {
	ID   string
	Hash string
}

// HashValue uses the Python contract's sorted, Unicode-preserving JSON with
// the default comma and colon spacing.
func HashValue(value any) (string, error) {
	encoded, err := pythonDefaultJSON(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func pythonDefaultJSON(value any) ([]byte, error) {
	compact, err := jsoncompat.MarshalPython(value, false)
	if err != nil {
		return nil, err
	}
	var spaced bytes.Buffer
	inString, escaped := false, false
	for _, char := range compact {
		if inString {
			spaced.WriteByte(char)
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		spaced.WriteByte(char)
		if char == '"' {
			inString = true
		} else if char == ',' || char == ':' {
			spaced.WriteByte(' ')
		}
	}
	return spaced.Bytes(), nil
}

func Path(repoRoot, workspaceID string) (string, error) {
	segment, err := paths.NormalizeWorkspaceID(workspaceID)
	if err != nil {
		return "", err
	}
	if segment == "" {
		return "", errors.New("supervisor ledger requires workspace_id")
	}
	return filepath.Join(repoRoot, "workspaces", segment, ".memory", "drive", "logs",
		"supervisor_ledger.jsonl"), nil
}

// Append writes a Python-readable supervisor event before returning a proof
// reference. The event ID and hash bind a concrete verifier result.
func Append(repoRoot, workspaceID, actor, stageID, tool string, result map[string]any) (Event, error) {
	resultHash, err := HashValue(result)
	if err != nil {
		return Event{}, err
	}
	return AppendWithResultHash(repoRoot, workspaceID, actor, stageID, tool, result, resultHash)
}

// AppendWithResultHash writes a ledger event when the typed result hash covers
// only a defined subset of the event result, as verification reports require.
func AppendWithResultHash(repoRoot, workspaceID, actor, stageID, tool string,
	result map[string]any, resultHash string) (Event, error) {
	if actor == "" || stageID == "" || tool == "" || result == nil {
		return Event{}, errors.New("supervisor event requires actor, stage, tool and result")
	}
	if len(resultHash) != 64 {
		return Event{}, errors.New("supervisor event requires a SHA-256 result hash")
	}
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Event{}, err
	}
	id := "ledger-" + hex.EncodeToString(random[:])
	return appendEvent(repoRoot, workspaceID, actor, stageID, tool, result, resultHash, id, false)
}

// AppendIdempotent binds an event to a durable operation identity. A retry
// after the ledger fsync reuses the original event even if the record write
// had not started when the process stopped.
func AppendIdempotent(repoRoot, workspaceID, actor, stageID, tool string,
	result map[string]any, identity string) (Event, error) {
	if identity == "" {
		return Event{}, errors.New("idempotent event requires an operation identity")
	}
	resultHash, err := HashValue(result)
	if err != nil {
		return Event{}, err
	}
	digest := sha256.Sum256([]byte(identity))
	id := "ledger-" + hex.EncodeToString(digest[:6])
	return appendEvent(repoRoot, workspaceID, actor, stageID, tool, result, resultHash, id, true)
}

func appendEvent(repoRoot, workspaceID, actor, stageID, tool string,
	result map[string]any, resultHash, id string, idempotent bool) (Event, error) {
	if actor == "" || stageID == "" || tool == "" || result == nil || len(resultHash) != 64 {
		return Event{}, errors.New("supervisor event requires actor, stage, tool, result and hash")
	}
	path, err := Path(repoRoot, workspaceID)
	if err != nil {
		return Event{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return Event{}, err
	}
	appendMu.Lock()
	defer appendMu.Unlock()
	processLock := flock.New(path + ".lock")
	lockCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	locked, err := processLock.TryLockContext(lockCtx, 50*time.Millisecond)
	if err != nil {
		return Event{}, err
	}
	if !locked {
		return Event{}, errors.New("supervisor ledger lock timed out")
	}
	defer processLock.Unlock()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return Event{}, err
	}
	defer file.Close()
	if idempotent {
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 4*1024*1024)
		for scanner.Scan() {
			var prior map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &prior); err != nil {
				return Event{}, errors.New("supervisor ledger contains a malformed event")
			}
			if prior["event_id"] != id {
				continue
			}
			priorHash, ok := prior["event_hash"].(string)
			if !ok || prior["workspace_id"] != workspaceID || prior["actor"] != actor ||
				prior["stage_id"] != stageID || prior["tool"] != tool ||
				prior["result_hash"] != resultHash {
				return Event{}, errors.New("supervisor operation event conflicts with prior event")
			}
			delete(prior, "event_hash")
			computed, err := HashValue(prior)
			if err != nil || computed != priorHash {
				return Event{}, errors.New("supervisor operation event hash is invalid")
			}
			return Event{ID: id, Hash: priorHash}, nil
		}
		if err := scanner.Err(); err != nil {
			return Event{}, err
		}
	}
	payload := map[string]any{"event_id": id, "ts": float64(time.Now().UnixNano()) / 1e9,
		"workspace_id": workspaceID, "actor": actor, "stage_id": stageID,
		"tool": tool, "result": result, "result_hash": resultHash}
	eventHash, err := HashValue(payload)
	if err != nil {
		return Event{}, err
	}
	payload["event_hash"] = eventHash
	line, err := json.Marshal(payload)
	if err != nil {
		return Event{}, err
	}
	_, writeErr := file.Write(append(line, '\n'))
	syncErr := file.Sync()
	if err := errors.Join(writeErr, syncErr); err != nil {
		return Event{}, err
	}
	return Event{ID: id, Hash: eventHash}, nil
}
