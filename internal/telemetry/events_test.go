package telemetry

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestMemoryEventIsContentFreeAndHashVerified(t *testing.T) {
	repo := t.TempDir()
	const target = "private-target-id"
	if err := Record(repo, Event{EventType: "memory_erase_rejected", WorkspaceID: "alpha",
		Status: "blocked", Error: "invalid_erase_authorization", Operation: "governed_erase",
		Decision: "authorization_checked", Outcome: "invalid_erase_authorization",
		SelectedIDs: []string{target}}); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(Path(repo))
	if err != nil || !bytes.HasSuffix(encoded, []byte{'\n'}) || strings.Contains(string(encoded), target) {
		t.Fatalf("memory event exposed target or was not flushed: %q %v", encoded, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(encoded), &payload); err != nil {
		t.Fatal(err)
	}
	actualHash, _ := payload["event_sha256"].(string)
	delete(payload, "event_sha256")
	expectedHash, err := eventHash(payload)
	if err != nil || expectedHash != actualHash {
		t.Fatalf("memory event hash changed: %q %q %v", actualHash, expectedHash, err)
	}
	selected := payload["selected_id_hashes"].([]any)
	if len(selected) != 1 || selected[0] != "889e07183b08c451" {
		t.Fatalf("memory event did not hash the target ID: %+v", selected)
	}
}
