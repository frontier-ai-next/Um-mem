package servicepolicy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"unicode/utf8"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
)

type pointer struct {
	RevisionID      string   `json:"revision_id"`
	Bundle          Bundle   `json:"bundle"`
	CatalogSnapshot Snapshot `json:"catalog_snapshot"`
}

func (entry pointer) valid() bool {
	if entry.Bundle.Validate() != nil || entry.RevisionID != entry.Bundle.RevisionID {
		return false
	}
	catalog, err := CatalogRevision(entry.CatalogSnapshot)
	return err == nil && catalog == entry.Bundle.CatalogRevisionID
}

type state struct {
	SchemaVersion      int                 `json:"schema_version"`
	Identity           map[string]string   `json:"identity"`
	Bundles            map[string]Bundle   `json:"bundles"`
	CatalogSnapshots   map[string]Snapshot `json:"catalog_snapshots"`
	Pointers           map[string]pointer  `json:"pointers"`
	SessionPins        map[string]string   `json:"session_pins"`
	ActivationReceipts map[string]any      `json:"activation_receipts"`
}

type Store struct {
	identity Identity
	path     string
	mu       sync.Mutex
}

func NewStore(identity Identity) (*Store, error) {
	identity, err := identity.normalized()
	if err != nil {
		return nil, err
	}
	path, err := StatePath(identity)
	if err != nil {
		return nil, err
	}
	return &Store{identity: identity, path: path}, nil
}

func (store *Store) Path() string { return store.path }

func (store *Store) Identity() Identity { return store.identity }

func (store *Store) read() (state, error) {
	raw, err := os.ReadFile(store.path)
	if err != nil {
		return state{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result state
	if err := decoder.Decode(&result); err != nil {
		return state{}, errors.New("service policy state is unreadable")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return state{}, errors.New("service policy state has trailing content")
	}
	if result.SchemaVersion != 1 || !reflect.DeepEqual(result.Identity, identityRecord(store.identity)) ||
		result.Bundles == nil || result.CatalogSnapshots == nil || result.Pointers == nil ||
		result.SessionPins == nil || result.ActivationReceipts == nil {
		return state{}, errors.New("service policy state schema or identity is invalid")
	}
	return result, nil
}

func (store *Store) write(current state) error {
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return err
	}
	encoded, err := jsoncompat.MarshalPython(current, true)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(store.path), "."+filepath.Base(store.path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(encoded); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), store.path)
}

func (current *state) record(entry pointer) {
	current.Bundles[entry.RevisionID] = entry.Bundle
	current.CatalogSnapshots[entry.RevisionID] = entry.CatalogSnapshot
}

func (current *state) named(name string) (pointer, bool) {
	entry, ok := current.Pointers[name]
	return entry, ok && entry.valid()
}

func (current *state) active() (pointer, bool, error) {
	if entry, ok := current.named("active"); ok {
		current.record(entry)
		return entry, false, nil
	}
	last, ok := current.named("last_known_good")
	if !ok {
		return pointer{}, false, errors.New("service policy state has no valid active or last-known-good bundle")
	}
	current.Pointers["active"] = last
	current.record(last)
	return last, true, nil
}

func (current *state) forRevision(revisionID string) (pointer, bool) {
	bundle, bundleOK := current.Bundles[revisionID]
	snapshot, snapshotOK := current.CatalogSnapshots[revisionID]
	if bundleOK && snapshotOK {
		entry := pointer{RevisionID: revisionID, Bundle: bundle, CatalogSnapshot: snapshot}
		if entry.valid() {
			return entry, true
		}
	}
	for _, name := range []string{"active", "last_known_good"} {
		if entry, ok := current.named(name); ok && entry.RevisionID == revisionID {
			current.record(entry)
			return entry, true
		}
	}
	return pointer{}, false
}

func cloneSnapshot(snapshot Snapshot) (Snapshot, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := json.Unmarshal(encoded, &rows); err != nil {
		return nil, err
	}
	return NormalizeSnapshot(rows)
}

// EnsureInitialized creates the baseline only once. An existing policy pin is
// authoritative even if the current catalog changed since it was created.
func (store *Store) EnsureInitialized(snapshot Snapshot) (Bundle, error) {
	normalized, err := NormalizeSnapshot(snapshot)
	if err != nil {
		return Bundle{}, err
	}
	baseline, err := Baseline(normalized)
	if err != nil {
		return Bundle{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.read()
	if err == nil {
		active, repaired, err := current.active()
		if err != nil {
			return Bundle{}, err
		}
		if repaired {
			if err := store.write(current); err != nil {
				return Bundle{}, err
			}
		}
		return active.Bundle, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Bundle{}, err
	}
	entry := pointer{RevisionID: baseline.RevisionID, Bundle: baseline, CatalogSnapshot: normalized}
	current = state{SchemaVersion: 1, Identity: identityRecord(store.identity),
		Bundles: map[string]Bundle{}, CatalogSnapshots: map[string]Snapshot{},
		Pointers:    map[string]pointer{"active": entry, "last_known_good": entry},
		SessionPins: map[string]string{}, ActivationReceipts: map[string]any{}}
	current.record(entry)
	if err := store.write(current); err != nil {
		return Bundle{}, err
	}
	return baseline, nil
}

func (store *Store) Active() (Bundle, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.read()
	if err != nil {
		return Bundle{}, err
	}
	active, repaired, err := current.active()
	if err != nil {
		return Bundle{}, err
	}
	if repaired {
		if err := store.write(current); err != nil {
			return Bundle{}, err
		}
	}
	return active.Bundle, nil
}

func sessionHash(sessionID string) (string, error) {
	if sessionID == "" || utf8.RuneCountInString(sessionID) > 4096 {
		return "", errors.New("session ID is required and must be bounded")
	}
	return sha("session:" + sessionID), nil
}

func (store *Store) PinSession(sessionID string) (Bundle, error) {
	key, err := sessionHash(sessionID)
	if err != nil {
		return Bundle{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.read()
	if err != nil {
		return Bundle{}, err
	}
	active, repaired, err := current.active()
	if err != nil {
		return Bundle{}, err
	}
	if revision, exists := current.SessionPins[key]; exists {
		entry, ok := current.forRevision(revision)
		if !ok {
			return Bundle{}, errors.New("service policy session pin is invalid")
		}
		if repaired {
			if err := store.write(current); err != nil {
				return Bundle{}, err
			}
		}
		return entry.Bundle, nil
	}
	current.SessionPins[key] = active.RevisionID
	current.record(active)
	if err := store.write(current); err != nil {
		return Bundle{}, err
	}
	return active.Bundle, nil
}

func (store *Store) Pinned(sessionID string) (*Bundle, error) {
	key, err := sessionHash(sessionID)
	if err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.read()
	if err != nil {
		return nil, err
	}
	revision, exists := current.SessionPins[key]
	if !exists {
		return nil, nil
	}
	entry, ok := current.forRevision(revision)
	if !ok {
		return nil, errors.New("service policy session pin is invalid")
	}
	return &entry.Bundle, nil
}

func (store *Store) SnapshotFor(revisionID string) (Snapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.read()
	if err != nil {
		return nil, err
	}
	entry, ok := current.forRevision(revisionID)
	if !ok {
		return nil, fmt.Errorf("policy bundle revision %q has no valid snapshot", revisionID)
	}
	return cloneSnapshot(entry.CatalogSnapshot)
}
