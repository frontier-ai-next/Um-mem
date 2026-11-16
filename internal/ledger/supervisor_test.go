package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestPythonDefaultJSONAndSupervisorEvent(t *testing.T) {
	value := map[string]any{"b": "🧠", "a": []any{1, map[string]any{"z": true}}}
	encoded, err := pythonDefaultJSON(value)
	if err != nil || string(encoded) != `{"a": [1, {"z": true}], "b": "🧠"}` {
		t.Fatalf("Python default JSON differs: %q, %v", encoded, err)
	}
	digest := sha256.Sum256(encoded)
	hash, err := HashValue(value)
	if err != nil || hash != hex.EncodeToString(digest[:]) ||
		hash != "e01053fd9a713fff4b2a1839c9199de1d62bef369558138789313b10a85034e9" {
		t.Fatalf("Python default hash differs: %q, %v", hash, err)
	}
	eventFixture := map[string]any{"event_id": "ledger-0123456789ab",
		"ts": 1761271234.1234567, "workspace_id": "alpha", "actor": "verifier",
		"stage_id": "exact_source_fidelity", "tool": "exact_source_fidelity",
		"result":      map[string]any{"text": "🧠", "passed": true},
		"result_hash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	fixtureHash, err := HashValue(eventFixture)
	if err != nil || fixtureHash != "ecad483585fa89927224464f46d1ee7bb6de5439b7c11a93ce7de83a3c94ccd6" {
		t.Fatalf("Python event hash differs: %q, %v", fixtureHash, err)
	}
	root := t.TempDir()
	event, err := Append(root, "alpha", "verifier", "exact_source_fidelity",
		"exact_source_fidelity", value)
	if err != nil {
		t.Fatal(err)
	}
	path, err := Path(root, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	line, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(line, &row); err != nil {
		t.Fatal(err)
	}
	if row["event_id"] != event.ID || row["event_hash"] != event.Hash || row["result_hash"] != hash {
		t.Fatalf("ledger ref differs from event: %+v", row)
	}
	delete(row, "event_hash")
	recalculated, err := HashValue(row)
	if err != nil || recalculated != event.Hash {
		t.Fatalf("ledger event hash does not bind row: %q, %v", recalculated, err)
	}
}
