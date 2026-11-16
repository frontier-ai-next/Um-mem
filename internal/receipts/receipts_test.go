package receipts

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestPythonReceiptReplayAndScopedGoLifecycle(t *testing.T) {
	fixture, err := os.ReadFile("testdata/python_receipts.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		Identity struct {
			WorkspaceID     string `json:"workspace_id"`
			ProfileID       string `json:"profile_id"`
			Backend         string `json:"backend"`
			CallerRole      string `json:"caller_role"`
			ProtocolVersion int    `json:"protocol_version"`
			OperationID     string `json:"mutation_correlation_id"`
		} `json:"identity"`
		Arguments map[string]any `json:"arguments"`
		Response  map[string]any `json:"response"`
		CreatedAt string         `json:"created_at"`
	}
	if err := json.Unmarshal(fixture, &expected); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	data, err := os.ReadFile("testdata/python_receipts.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(Path(root)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root), data, 0o600); err != nil {
		t.Fatal(err)
	}
	identity := Identity{WorkspaceID: expected.Identity.WorkspaceID,
		ProfileID: expected.Identity.ProfileID, Backend: expected.Identity.Backend,
		CallerRole: expected.Identity.CallerRole, ProtocolVersion: expected.Identity.ProtocolVersion}
	ctx := context.Background()
	stored, err := Status(ctx, root, identity, expected.Identity.OperationID)
	if err != nil || stored == nil || stored.Status != "committed" ||
		stored.CreatedAt != expected.CreatedAt || !reflect.DeepEqual(stored.Response, expected.Response) {
		t.Fatalf("Python receipt was not readable in Go: %+v, %v", stored, err)
	}
	claimed, err := Claim(ctx, root, identity, expected.Identity.OperationID, expected.Arguments)
	if err != nil || claimed.Status != "committed" || !reflect.DeepEqual(claimed.Response, expected.Response) {
		t.Fatalf("Python receipt was not replayed in Go: %+v, %v", claimed, err)
	}
	if _, err := Claim(ctx, root, identity, expected.Identity.OperationID, map[string]any{"tool": "remember", "request": map[string]any{"content": "changed"}}); err == nil {
		t.Fatal("changed request reused a committed Python operation ID")
	}
	other := identity
	other.ProfileID = "other"
	if foreign, err := Status(ctx, root, other, expected.Identity.OperationID); err != nil || foreign != nil {
		t.Fatalf("receipt crossed profile boundary: %+v, %v", foreign, err)
	}

	newID := "new-go-operation"
	first, err := Claim(ctx, root, identity, newID, expected.Arguments)
	if err != nil || first.Status != "new" || first.CreatedAt == "" {
		t.Fatalf("Go receipt claim failed: %+v, %v", first, err)
	}
	second, err := Claim(ctx, root, identity, newID, expected.Arguments)
	if err != nil || second.Status != "pending" || second.CreatedAt != first.CreatedAt {
		t.Fatalf("Go pending claim was not stable: %+v, %v", second, err)
	}
	if err := Complete(ctx, root, identity, newID, expected.Arguments, expected.Response, "committed"); err != nil {
		t.Fatal(err)
	}
	if err := Complete(ctx, root, identity, newID, expected.Arguments, expected.Response, "committed"); err != nil {
		t.Fatalf("identical completion was not idempotent: %v", err)
	}
	changed := map[string]any{}
	for key, value := range expected.Response {
		changed[key] = value
	}
	changed["revision_id"] = "different"
	if err := Complete(ctx, root, identity, newID, expected.Arguments, changed, "committed"); err == nil {
		t.Fatal("changed completion replaced the committed response")
	}
	failedID := "failed-go-operation"
	if _, err := Claim(ctx, root, identity, failedID, expected.Arguments); err != nil {
		t.Fatal(err)
	}
	failure := map[string]any{"ok": false, "error_type": "ValueError", "reason_code": "native_unavailable",
		"error_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	if err := Complete(ctx, root, identity, failedID, expected.Arguments, failure, "failed"); err != nil {
		t.Fatal(err)
	}
	if retried, err := Claim(ctx, root, identity, failedID, expected.Arguments); err != nil || retried.Status != "pending" {
		t.Fatalf("failed receipt was not reopened for retry: %+v, %v", retried, err)
	}
}

func TestConcurrentClaimHasSingleNewOwner(t *testing.T) {
	root := t.TempDir()
	identity := Identity{WorkspaceID: "alpha", ProfileID: "team", Backend: "hindsight", CallerRole: "go_test", ProtocolVersion: 2}
	arguments := map[string]any{"tool": "remember", "request": map[string]any{"content": "same"}}
	const callers = 8
	results := make(chan State, callers)
	errorsOut := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Go(func() {
			state, err := Claim(context.Background(), root, identity, "shared-operation", arguments)
			results <- state
			errorsOut <- err
		})
	}
	group.Wait()
	close(results)
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatal(err)
		}
	}
	newCount := 0
	createdAt := ""
	for result := range results {
		if result.Status == "new" {
			newCount++
		} else if result.Status != "pending" {
			t.Fatalf("unexpected concurrent claim state: %+v", result)
		}
		if createdAt == "" {
			createdAt = result.CreatedAt
		} else if result.CreatedAt != createdAt {
			t.Fatalf("concurrent claim changed creation timestamp: %+v", result)
		}
	}
	if newCount != 1 {
		t.Fatalf("concurrent claim created %d owners", newCount)
	}
}

func TestMalformedReceiptFailsClosedWithoutCreatingOnRead(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	identity := Identity{WorkspaceID: "alpha", ProfileID: "team", Backend: "hindsight", CallerRole: "go_test", ProtocolVersion: 2}
	if state, err := Status(ctx, root, identity, "absent"); err != nil || state != nil {
		t.Fatalf("absent receipt was reported: %+v, %v", state, err)
	}
	if _, err := os.Stat(Path(root)); !os.IsNotExist(err) {
		t.Fatal("read-only status created a receipts database")
	}
	arguments := map[string]any{"tool": "remember"}
	if _, err := Claim(ctx, root, identity, "operation", arguments); err != nil {
		t.Fatal(err)
	}
	response := map[string]any{"receipt_kind": "mutation_response", "mutation_tool": "remember",
		"response": map[string]any{"ok": true, "memory_id": "mem_1"}}
	if err := Complete(ctx, root, identity, "operation", arguments, response, "committed"); err != nil {
		t.Fatal(err)
	}
	db, err := open(ctx, Path(root), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE remember_receipts SET response_json='{}'"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if state, err := Status(ctx, root, identity, "operation"); err == nil || state != nil {
		t.Fatalf("corrupt committed receipt was accepted: %+v, %v", state, err)
	}
}

func TestFingerprintPreservesLargeJSONIntegers(t *testing.T) {
	root := t.TempDir()
	identity := Identity{WorkspaceID: "alpha", ProfileID: "team", Backend: "hindsight", CallerRole: "go_test", ProtocolVersion: 2}
	first := map[string]any{"request": map[string]any{"source_id": json.Number("9007199254740992")}}
	second := map[string]any{"request": map[string]any{"source_id": json.Number("9007199254740993")}}
	if _, err := Claim(context.Background(), root, identity, "numeric-operation", first); err != nil {
		t.Fatal(err)
	}
	if _, err := Claim(context.Background(), root, identity, "numeric-operation", second); err == nil {
		t.Fatal("distinct large JSON integers shared one operation fingerprint")
	}
}
