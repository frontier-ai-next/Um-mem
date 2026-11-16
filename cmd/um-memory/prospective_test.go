package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestProspectiveCLIJourney(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()
	base := []string{"--repo-root", repo, "--workspace", "alpha", "--profile", "team", "--backend", "hindsight"}
	requestFile := filepath.Join(t.TempDir(), "request.json")
	call := func(command string, request any, extra ...string) (map[string]any, error) {
		t.Helper()
		args := append([]string{command}, base...)
		if request != nil {
			encoded, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(requestFile, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			args = append(args, "--request-file", requestFile)
		}
		args = append(args, extra...)
		var output bytes.Buffer
		err := run(ctx, args, &output)
		if err != nil {
			return nil, err
		}
		var result map[string]any
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result, nil
	}
	hash := strings.Repeat("a", 64)
	ref := memory.EvidenceRef{RefType: "session_event", RefID: "evt:1", Hash: &hash,
		ProducedBy: "harness", ProducerID: "host:1"}
	value := memory.ProspectiveObligation{ObligationID: "obl:cli", Scope: "run", ScopeID: "run:1",
		EvidenceRefs: []memory.EvidenceRef{ref}, RequiredIntervention: "Review the tool call",
		TriggerPredicates: []memory.ProspectivePredicate{{PredicateType: "registered_condition", Field: "signals",
			Operator: "contains", Value: "codex_event:PreToolUse"}}}
	created, err := call("prospective-create", map[string]any{"obligation": value, "event_id": "evt:create"})
	if err != nil || created["status"] != "pending" {
		t.Fatalf("create: %+v %v", created, err)
	}
	arm := memory.ProspectiveLifecycleEvent{EventID: "evt:arm", EventType: "arm", ExpectedVersion: 0,
		ExpectedStatus: "pending", EvidenceRefs: []memory.EvidenceRef{ref}}
	armed, err := call("prospective-transition", map[string]any{"obligation_id": value.ObligationID, "event": arm})
	if err != nil || armed["status"] != "armed" {
		t.Fatalf("arm: %+v %v", armed, err)
	}
	triggerRequest := map[string]any{"scope": "run", "scope_id": "run:1",
		"registered_condition": map[string]any{"signals": "codex_event:PreToolUse"}}
	encodedTrigger, err := json.Marshal(triggerRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestFile, encodedTrigger, 0600); err != nil {
		t.Fatal(err)
	}
	var candidateOutput bytes.Buffer
	if err := run(ctx, append(append([]string{"prospective-candidates"}, base...), "--request-file", requestFile), &candidateOutput); err != nil {
		t.Fatal(err)
	}
	var candidates []map[string]any
	if err := json.Unmarshal(candidateOutput.Bytes(), &candidates); err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0]["matched"] != true || candidates[0]["host_action_authorized"] != false {
		t.Fatalf("candidate decision: %+v", candidates)
	}
	fire := memory.ProspectiveLifecycleEvent{EventID: "evt:fire", EventType: "fire", ExpectedVersion: 1,
		ExpectedStatus: "armed", EvidenceRefs: []memory.EvidenceRef{ref}}
	if _, err := call("prospective-transition", map[string]any{"obligation_id": value.ObligationID, "event": fire}); err == nil {
		t.Fatal("CLI accepted direct fire")
	}
	fired, err := call("prospective-apply", map[string]any{"obligation_id": value.ObligationID, "event": fire,
		"trigger_context": map[string]any{
			"trigger":           triggerRequest,
			"origin":            map[string]any{"channel": "host_event", "principal_id": "host:1", "integrity_class": "observed"},
			"authority_ceiling": "advisory"}})
	if err != nil || fired["status"] != "fired" {
		t.Fatalf("fire: %+v %v", fired, err)
	}
	intervention, err := call("prospective-intervention", nil, "--obligation-id", value.ObligationID)
	if err != nil || intervention["text"] != "Review the tool call" || intervention["host_action_authorized"] != false {
		t.Fatalf("intervention: %+v %v", intervention, err)
	}
	report, err := call("prospective-telemetry", nil)
	if err != nil || report["channel"] != "prospective_obligation_telemetry" || report["ordinary_recall"] != false ||
		report["host_action_authorized"] != false {
		t.Fatalf("telemetry: %+v %v", report, err)
	}
	entries, ok := report["entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("telemetry entries: %+v", report)
	}
}
