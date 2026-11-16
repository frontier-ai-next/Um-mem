package servicepolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPolicyStoreSessionPinSurvivesRestartAndRepairsActive(t *testing.T) {
	identity := Identity{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", Backend: "mempalace"}
	snapshot, err := NormalizeSnapshot([]map[string]any{{"block_id": "example", "name": "Example"}})
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(identity)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := store.EnsureInitialized(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := store.PinSession("session-1")
	if err != nil || pinned.RevisionID != baseline.RevisionID {
		t.Fatalf("session did not pin the baseline: %+v, %v", pinned, err)
	}
	restarted, err := NewStore(identity)
	if err != nil {
		t.Fatal(err)
	}
	retained, err := restarted.Pinned("session-1")
	if err != nil || retained == nil || retained.RevisionID != baseline.RevisionID {
		t.Fatalf("session pin was lost after restart: %+v, %v", retained, err)
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	pins := state["session_pins"].(map[string]any)
	if len(pins) != 1 || pins[sha("session:session-1")] != baseline.RevisionID {
		t.Fatalf("session pin exposed its raw identity or wrong revision: %+v", pins)
	}
	pointers := state["pointers"].(map[string]any)
	pointers["active"] = map[string]any{"invalid": true}
	broken, _ := json.Marshal(state)
	if err := os.WriteFile(store.Path(), broken, 0o600); err != nil {
		t.Fatal(err)
	}
	repaired, err := restarted.Active()
	if err != nil || repaired.RevisionID != baseline.RevisionID {
		t.Fatalf("last-known-good pointer did not recover active state: %+v, %v", repaired, err)
	}
	if _, err := os.Stat(filepath.Join(identity.RepoRoot, ".UM-MEM", "memory", "service_policy_bundles")); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyStageActivationAndExplicitSessionReassignment(t *testing.T) {
	store, err := NewStore(Identity{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", Backend: "mempalace"})
	if err != nil {
		t.Fatal(err)
	}
	firstSnapshot, _ := NormalizeSnapshot([]map[string]any{{"block_id": "first"}})
	first, err := store.EnsureInitialized(firstSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PinSession("existing"); err != nil {
		t.Fatal(err)
	}
	secondSnapshot, _ := NormalizeSnapshot([]map[string]any{{"block_id": "second"}})
	second, err := Baseline(secondSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stage(second, secondSnapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PinSessionToBundle("staged", second.RevisionID); err != nil {
		t.Fatal(err)
	}
	active, err := store.Active()
	if err != nil || active.RevisionID != first.RevisionID {
		t.Fatalf("staging changed active policy: %+v, %v", active, err)
	}
	receipt, err := store.Activate(second, "activation-1", nil)
	if err != nil || !receipt.Accepted() || receipt.ActiveRevisionID != second.RevisionID {
		t.Fatalf("staged bundle did not activate: %+v, %v", receipt, err)
	}
	replay, err := store.Activate(second, "activation-1", nil)
	if err != nil || replay != receipt {
		t.Fatalf("activation replay changed receipt: %+v, %v", replay, err)
	}
	oldPin, err := store.Pinned("existing")
	if err != nil || oldPin == nil || oldPin.RevisionID != first.RevisionID {
		t.Fatalf("activation silently moved an existing session: %+v, %v", oldPin, err)
	}
	moved, err := store.ReassignPins(first.RevisionID, second.RevisionID)
	if err != nil || moved != 1 {
		t.Fatalf("explicit reassignment failed: %d, %v", moved, err)
	}
	oldPin, err = store.Pinned("existing")
	if err != nil || oldPin == nil || oldPin.RevisionID != second.RevisionID {
		t.Fatalf("session did not receive explicitly reassigned policy: %+v, %v", oldPin, err)
	}
	thirdSnapshot, _ := NormalizeSnapshot([]map[string]any{{"block_id": "third"}})
	third, _ := Baseline(thirdSnapshot)
	rejected, err := store.Activate(third, "activation-2", nil)
	if err != nil || rejected.Status != "rejected" || rejected.Reason != "catalog_snapshot_required_for_changed_catalog" {
		t.Fatalf("changed catalog activated without a snapshot: %+v, %v", rejected, err)
	}
}

func TestPolicyInteropExportWhenRequested(t *testing.T) {
	root := os.Getenv("UM_MEM_POLICY_INTEROP_ROOT")
	if root == "" {
		t.Skip("cross-language export root was not provided")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(Identity{RepoRoot: root, WorkspaceID: "alpha", ProfileID: "team", Backend: "mempalace"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := NormalizeSnapshot([]map[string]any{{"block_id": "example", "name": "Example"}})
	bundle, err := store.EnsureInitialized(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PinSession("session-1"); err != nil {
		t.Fatal(err)
	}
	t.Logf("policy=%s path=%s", bundle.RevisionID, store.Path())
}
