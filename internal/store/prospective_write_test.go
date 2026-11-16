package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func prospectiveTestRef() memory.EvidenceRef {
	hash := strings.Repeat("a", 64)
	return memory.EvidenceRef{RefType: "session_event", RefID: "evt:1", Hash: &hash,
		ProducedBy: "harness", ProducerID: "host:1"}
}

func prospectiveTestObligation() memory.ProspectiveObligation {
	return memory.ProspectiveObligation{ObligationID: "obl-1", Scope: "run", ScopeID: "run:1",
		EvidenceRefs:         []memory.EvidenceRef{prospectiveTestRef()},
		RequiredIntervention: "Review before tool use",
		TriggerPredicates: []memory.ProspectivePredicate{{PredicateType: "registered_condition", Field: "signals",
			Operator: "contains", Value: "codex_event:PreToolUse"}}}
}

func TestProspectiveWriterLifecycleAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path, "alpha", "team", "mempalace")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	index, err := OpenIndex(path, "alpha", "team")
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	ctx := context.Background()
	initial := prospectiveTestObligation()
	created, err := writer.CreateProspectiveObligation(ctx, initial, "evt:create", "")
	if err != nil || created.Version != 0 || created.Status != "pending" || created.Replayed {
		t.Fatalf("create: %+v %v", created, err)
	}
	again, err := writer.CreateProspectiveObligation(ctx, initial, "evt:create", "")
	if err != nil || !again.Replayed || again.ReceiptID != created.ReceiptID {
		t.Fatalf("create replay: %+v %v", again, err)
	}
	if _, err := writer.CreateProspectiveObligation(ctx, initial, "evt:different", ""); err == nil {
		t.Fatal("existing obligation accepted a new create event")
	}
	if _, err := writer.CreateProspectiveObligation(ctx, initial, "evt:create", "different-key"); err == nil {
		t.Fatal("create event accepted changed idempotency key")
	}
	arm := memory.ProspectiveLifecycleEvent{EventID: "evt:arm", EventType: "arm", ExpectedVersion: 0,
		ExpectedStatus: "pending", EvidenceRefs: []memory.EvidenceRef{prospectiveTestRef()}}
	armed, err := writer.TransitionProspectiveObligation(ctx, initial.ObligationID, arm)
	if err != nil || armed.Version != 1 || armed.Status != "armed" || armed.Replayed {
		t.Fatalf("arm: %+v %v", armed, err)
	}
	fixtureRaw, err := os.ReadFile(filepath.Join("testdata", "python_prospective.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		PayloadJSON   string `json:"payload_json"`
		PayloadID     string `json:"payload_id"`
		PayloadSHA256 string `json:"payload_sha256"`
		ReceiptID     string `json:"receipt_id"`
	}
	if err := json.Unmarshal(fixtureRaw, &fixture); err != nil {
		t.Fatal(err)
	}
	var actualPayload, actualID, actualSHA, actualReceipt string
	if err := writer.db.QueryRowContext(ctx, `SELECT p.payload_json,o.payload_id,o.payload_sha256,o.receipt_id
	 FROM prospective_obligations o JOIN canonical_payloads p ON p.payload_id=o.payload_id WHERE o.obligation_id=?`, initial.ObligationID).
		Scan(&actualPayload, &actualID, &actualSHA, &actualReceipt); err != nil {
		t.Fatal(err)
	}
	if actualPayload != fixture.PayloadJSON || actualID != fixture.PayloadID || actualSHA != fixture.PayloadSHA256 || actualReceipt != fixture.ReceiptID {
		t.Fatal("Go prospective transition differs from Python fixture bytes or IDs")
	}
	read, err := index.GetProspectiveObligation(ctx, initial.ObligationID)
	if err != nil || read == nil || read.Status != "armed" {
		t.Fatalf("read: %+v %v", read, err)
	}
	candidates, err := index.ProspectiveObligations(ctx, 50)
	if err != nil || len(candidates) != 1 || candidates[0].Status != "armed" {
		t.Fatalf("trigger index: %+v %v", candidates, err)
	}
	armReplay, err := writer.TransitionProspectiveObligation(ctx, initial.ObligationID, arm)
	if err != nil || !armReplay.Replayed || armReplay.ReceiptID != armed.ReceiptID {
		t.Fatalf("arm replay: %+v %v", armReplay, err)
	}
	if _, err := writer.TransitionProspectiveObligation(ctx, initial.ObligationID,
		memory.ProspectiveLifecycleEvent{EventID: "evt:stale", EventType: "arm", ExpectedVersion: 0,
			ExpectedStatus: "pending", EvidenceRefs: []memory.EvidenceRef{prospectiveTestRef()}}); err == nil {
		t.Fatal("stale transition accepted")
	}
	fire := memory.ProspectiveLifecycleEvent{EventID: "evt:fire", EventType: "fire", ExpectedVersion: 1,
		ExpectedStatus: "armed", EvidenceRefs: []memory.EvidenceRef{prospectiveTestRef()}}
	fired, err := writer.TransitionProspectiveObligation(ctx, initial.ObligationID, fire)
	if err != nil || fired.Version != 2 || fired.Status != "fired" {
		t.Fatalf("fire: %+v %v", fired, err)
	}
	oldReplay, err := writer.TransitionProspectiveObligation(ctx, initial.ObligationID, arm)
	if err != nil || !oldReplay.Replayed || oldReplay.Status != "armed" || oldReplay.Version != 1 {
		t.Fatalf("older replay: %+v %v", oldReplay, err)
	}
	satisfy := memory.ProspectiveLifecycleEvent{EventID: "evt:satisfy", EventType: "satisfy", ExpectedVersion: 2,
		ExpectedStatus: "fired", EvidenceRefs: []memory.EvidenceRef{prospectiveTestRef()},
		SatisfactionEvidenceRefs: []memory.EvidenceRef{prospectiveTestRef()},
		SatisfactionVerifier:     "test", SatisfactionVerifierResult: "passed"}
	satisfied, err := writer.TransitionProspectiveObligation(ctx, initial.ObligationID, satisfy)
	if err != nil || satisfied.Version != 3 || satisfied.Status != "satisfied" {
		t.Fatalf("satisfy: %+v %v", satisfied, err)
	}
	if _, err := writer.TransitionProspectiveObligation(ctx, initial.ObligationID,
		memory.ProspectiveLifecycleEvent{EventID: "evt:invalid", EventType: "fire", ExpectedVersion: 3,
			ExpectedStatus: "satisfied", EvidenceRefs: []memory.EvidenceRef{prospectiveTestRef()}}); err == nil {
		t.Fatal("transition out of terminal state accepted")
	}
}

func TestProspectiveNodeMatchesPythonShape(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "python_prospective.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		NodeJSON    string `json:"node_json"`
		PayloadJSON string `json:"payload_json"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	var value memory.ProspectiveObligation
	if err := decodeProspectiveJSON(fixture.PayloadJSON, &value); err != nil {
		t.Fatal(err)
	}
	value, err = value.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	node := prospectiveNode(value, "alpha", 1, "2026-10-24T04:49:33Z", "2026-10-24T04:49:33Z")
	encoded, err := jsoncompat.MarshalPythonDefault(node, false)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != fixture.NodeJSON {
		t.Fatalf("node differs from Python:\n%s\n%s", encoded, fixture.NodeJSON)
	}
}

func TestProspectiveCreateRollsBackWhenTriggerIndexConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	if err := CreateIndex(path); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenWriter(path, "alpha", "team", "mempalace")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	value := prospectiveTestObligation()
	id := prospectiveNodeID("alpha", value.ObligationID)
	if _, err := writer.db.Exec(`INSERT INTO index_nodes(node_id,workspace_id,node_type,title,tags_json,payload_json,updated_at)
	 VALUES(?,?,?,?,?,?,?)`, id, "foreign", "prospective_trigger", "bad", "[]", `{"node_id":"bad","workspace_id":"foreign","node_type":"prospective_trigger"}`, "2026-10-24T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CreateProspectiveObligation(context.Background(), value, "evt:create", ""); err == nil {
		t.Fatal("conflicting prospective trigger index was overwritten")
	}
	var count int
	if err := writer.db.QueryRow(`SELECT COUNT(*) FROM prospective_obligations WHERE obligation_id=?`, value.ObligationID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed prospective create left a canonical state row")
	}
	if err := writer.db.QueryRow(`SELECT COUNT(*) FROM prospective_obligation_events WHERE obligation_id=?`, value.ObligationID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed prospective create left an event")
	}
}
