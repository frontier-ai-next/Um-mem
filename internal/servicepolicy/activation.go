package servicepolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type ActivationReceipt struct {
	ActivationIDSHA256      string `json:"activation_id_sha256"`
	RequestedRevisionID     string `json:"requested_revision_id"`
	ActiveRevisionID        string `json:"active_revision_id"`
	LastKnownGoodRevisionID string `json:"last_known_good_revision_id"`
	Status                  string `json:"status"`
	Reason                  string `json:"reason"`
}

func (receipt ActivationReceipt) Accepted() bool { return receipt.Status == "activated" }

func (receipt ActivationReceipt) Validate() error {
	for _, value := range []string{receipt.ActivationIDSHA256, receipt.RequestedRevisionID,
		receipt.ActiveRevisionID, receipt.LastKnownGoodRevisionID} {
		if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
			return errors.New("policy activation receipt has an invalid digest")
		}
		for _, char := range value[len("sha256:"):] {
			if char < '0' || char > '9' && char < 'a' || char > 'f' {
				return errors.New("policy activation receipt has an invalid digest")
			}
		}
	}
	if receipt.Status != "activated" && receipt.Status != "rejected" {
		return errors.New("policy activation receipt status is invalid")
	}
	if receipt.Reason != "" && !identifierPattern.MatchString(receipt.Reason) {
		return errors.New("policy activation receipt reason is invalid")
	}
	return nil
}

func (store *Store) LastKnownGood() (Bundle, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.read()
	if err != nil {
		return Bundle{}, err
	}
	if entry, ok := current.named("last_known_good"); ok {
		current.record(entry)
		return entry.Bundle, nil
	}
	active, ok := current.named("active")
	if !ok {
		return Bundle{}, errors.New("service policy state has no valid active or last-known-good bundle")
	}
	current.Pointers["last_known_good"] = active
	current.record(active)
	if err := store.write(current); err != nil {
		return Bundle{}, err
	}
	return active.Bundle, nil
}

func (store *Store) BundleFor(revisionID string) (Bundle, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.read()
	if err != nil {
		return Bundle{}, err
	}
	entry, ok := current.forRevision(revisionID)
	if !ok {
		return Bundle{}, errors.New("policy bundle revision is not persisted")
	}
	return entry.Bundle, nil
}

func (store *Store) Stage(bundle Bundle, snapshot Snapshot) (Bundle, error) {
	if err := bundle.Validate(); err != nil {
		return Bundle{}, err
	}
	snapshot, err := NormalizeSnapshot(snapshot)
	if err != nil {
		return Bundle{}, err
	}
	entry := pointer{RevisionID: bundle.RevisionID, Bundle: bundle, CatalogSnapshot: snapshot}
	if !entry.valid() {
		return Bundle{}, errors.New("catalog_snapshot_revision_mismatch")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.read()
	if err != nil {
		return Bundle{}, err
	}
	active, _, err := current.active()
	if err != nil {
		return Bundle{}, err
	}
	current.record(active)
	current.record(entry)
	if err := store.write(current); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

func (store *Store) PinSessionToBundle(sessionID, revisionID string) (Bundle, error) {
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
	entry, ok := current.forRevision(revisionID)
	if !ok {
		return Bundle{}, errors.New("policy bundle revision is not staged")
	}
	current.SessionPins[key] = revisionID
	current.record(entry)
	if err := store.write(current); err != nil {
		return Bundle{}, err
	}
	return entry.Bundle, nil
}

func (store *Store) ReassignPins(fromRevisionID, toRevisionID string) (int, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.read()
	if err != nil {
		return 0, err
	}
	target, targetOK := current.forRevision(toRevisionID)
	_, sourceOK := current.forRevision(fromRevisionID)
	if !targetOK || !sourceOK {
		return 0, errors.New("policy bundle revision is not persisted")
	}
	count := 0
	for session, revision := range current.SessionPins {
		if revision == fromRevisionID {
			current.SessionPins[session] = toRevisionID
			count++
		}
	}
	current.record(target)
	if err := store.write(current); err != nil {
		return 0, err
	}
	return count, nil
}

func (store *Store) activationSnapshot(current *state, bundle Bundle, supplied Snapshot) (Snapshot, error) {
	if supplied != nil {
		return NormalizeSnapshot(supplied)
	}
	if entry, ok := current.forRevision(bundle.RevisionID); ok {
		return entry.CatalogSnapshot, nil
	}
	active, _, err := current.active()
	if err != nil {
		return nil, err
	}
	if active.Bundle.CatalogRevisionID == bundle.CatalogRevisionID {
		return active.CatalogSnapshot, nil
	}
	return nil, errors.New("catalog_snapshot_required_for_changed_catalog")
}

func (store *Store) Activate(bundle Bundle, activationID string, supplied Snapshot) (ActivationReceipt, error) {
	if err := bundle.Validate(); err != nil {
		return ActivationReceipt{}, err
	}
	if activationID == "" || len([]rune(activationID)) > 4096 {
		return ActivationReceipt{}, errors.New("activation ID is required and must be bounded")
	}
	activationHash := "sha256:" + sha("activation:"+activationID)
	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.read()
	if err != nil {
		return ActivationReceipt{}, err
	}
	active, repaired, err := current.active()
	if err != nil {
		return ActivationReceipt{}, err
	}
	if raw, exists := current.ActivationReceipts[activationHash]; exists {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return ActivationReceipt{}, err
		}
		var receipt ActivationReceipt
		if err := json.Unmarshal(encoded, &receipt); err != nil || receipt.Validate() != nil {
			return ActivationReceipt{}, errors.New("service policy activation receipt is invalid")
		}
		if receipt.RequestedRevisionID != bundle.RevisionID {
			return ActivationReceipt{}, errors.New("service policy activation receipt conflict")
		}
		if repaired {
			if err := store.write(current); err != nil {
				return ActivationReceipt{}, err
			}
		}
		return receipt, nil
	}
	snapshot, snapshotErr := store.activationSnapshot(&current, bundle, supplied)
	if snapshotErr == nil {
		entry := pointer{RevisionID: bundle.RevisionID, Bundle: bundle, CatalogSnapshot: snapshot}
		if !entry.valid() {
			snapshotErr = errors.New("catalog_snapshot_revision_mismatch")
		}
	}
	receipt := ActivationReceipt{ActivationIDSHA256: activationHash,
		RequestedRevisionID: bundle.RevisionID, ActiveRevisionID: active.RevisionID,
		LastKnownGoodRevisionID: active.RevisionID}
	if snapshotErr != nil {
		receipt.Status = "rejected"
		reason := snapshotErr.Error()
		if !identifierPattern.MatchString(reason) {
			reason = "invalid_policy_bundle_activation"
		}
		receipt.Reason = reason
		current.ActivationReceipts[activationHash] = receipt
		if repaired {
			current.record(active)
		}
	} else {
		entry := pointer{RevisionID: bundle.RevisionID, Bundle: bundle, CatalogSnapshot: snapshot}
		current.record(entry)
		current.Pointers["active"] = entry
		current.Pointers["last_known_good"] = entry
		receipt.ActiveRevisionID, receipt.LastKnownGoodRevisionID = bundle.RevisionID, bundle.RevisionID
		receipt.Status = "activated"
		current.ActivationReceipts[activationHash] = receipt
	}
	if err := receipt.Validate(); err != nil {
		return ActivationReceipt{}, fmt.Errorf("build policy activation receipt: %w", err)
	}
	if err := store.write(current); err != nil {
		return ActivationReceipt{}, err
	}
	return receipt, nil
}
