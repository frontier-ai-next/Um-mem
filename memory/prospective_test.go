package memory

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProspectiveCanonicalPayloadMatchesPython(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "internal", "store", "testdata", "python_prospective.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		PayloadJSON string `json:"payload_json"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewBufferString(fixture.PayloadJSON))
	decoder.UseNumber()
	var obligation ProspectiveObligation
	if err := decoder.Decode(&obligation); err != nil {
		t.Fatal(err)
	}
	actual, err := obligation.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != fixture.PayloadJSON {
		t.Fatalf("prospective payload differs from Python:\n%s\n%s", actual, fixture.PayloadJSON)
	}
}

func TestProspectiveTextNormalizationMatchesPythonFieldContracts(t *testing.T) {
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	value := ProspectiveObligation{ObligationID: " id:e\u0301 ", Scope: "workspace",
		EvidenceRefs: []EvidenceRef{{RefType: "session_event", RefID: "evt:1", Hash: &hash,
			ProducedBy: "harness", ProducerID: "host:1"}},
		RequiredIntervention: " Re\u0301view ", SemanticTriggerCues: []string{" cue:e\u0301 "}}
	normalized, err := value.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if normalized.ObligationID != "id:e\u0301" || normalized.SemanticTriggerCues[0] != "cue:e\u0301" ||
		normalized.RequiredIntervention != "Réview" {
		t.Fatalf("Python field normalization changed: %+v", normalized)
	}
}

func TestProspectiveTelemetryIsImmutableAndRejectsNegativeDelta(t *testing.T) {
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	value := ProspectiveObligation{ObligationID: "obl:telemetry", Scope: "workspace",
		EvidenceRefs: []EvidenceRef{{RefType: "session_event", RefID: "evt:1", Hash: &hash,
			ProducedBy: "harness", ProducerID: "host:1"}}}
	updated, err := value.WithTelemetry(ProspectiveTelemetry{FalsePositiveCount: 2, LateTriggerCount: 1})
	if err != nil || updated.Telemetry.FalsePositiveCount != 2 || updated.Telemetry.LateTriggerCount != 1 ||
		value.Telemetry.FalsePositiveCount != 0 {
		t.Fatalf("telemetry update: %+v %v", updated, err)
	}
	if _, err := updated.WithTelemetry(ProspectiveTelemetry{MissedTriggerCount: -1}); err == nil {
		t.Fatal("negative telemetry delta was accepted")
	}
}
