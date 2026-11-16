package memory

import "testing"

func TestProcedureReuseRequiresSameSignatureOrQualifiedTransfer(t *testing.T) {
	same, err := QualifyProcedureReuse("procedure-1", "repair:python", "repair:python", nil)
	if err != nil || !same.Allowed || same.Reason != "same_task_signature" ||
		same.PromotionAuthorized || same.HostActionAuthorized {
		t.Fatalf("same-task reuse decision: %+v %v", same, err)
	}
	denied, err := QualifyProcedureReuse("procedure-1", "repair:python", "repair:rust", nil)
	if err != nil || denied.Allowed || denied.Reason != "cross_task_signature_requires_qualified_transfer" {
		t.Fatalf("cross-task reuse was allowed: %+v %v", denied, err)
	}
	envelope := &TransferEnvelope{EnvelopeID: "transfer-procedure-1", Status: "accepted",
		EvidenceRefs: []EvidenceRef{{RefType: "verification_report", RefID: "report-1",
			ProducedBy: "verifier", ProducerID: "host:verifier"}},
		SourceScope: "workspace:um-mem", TargetScope: "workspace:rust-port",
		SourceTaskSignature: "repair:python", TargetTaskSignature: "repair:rust",
		QualifiedMemoryIDs:  []string{"procedure-1"},
		VerifiedOutcomeIDs:  []string{"credit-a", "credit-b"},
		IndependenceDomains: []string{"project:um-mem", "project:rust-port"},
		SourceWorkflow:      "repair-python", TargetWorkflow: "repair-rust",
		SourceProjectID: "um-mem", TargetProjectID: "rust-port",
		SourceHost: "hermes", TargetHost: "codex",
		SourceProfile: "hermes:deep", TargetProfile: "codex:deep",
		SourceStages: []string{"diagnose", "repair", "verify"},
		TargetStages: []string{"diagnose", "repair", "verify"},
		SupportCount: 2, SourceRevisionID: "um-mem@abc123",
		ValidFrom: "2026-01-01T00:00:00Z", ExpiresAt: "2027-01-01T00:00:00Z",
		DriftFingerprint: "sha256:transfer-procedure-1"}
	transferred, err := QualifyProcedureReuse("procedure-1", "repair:python", "repair:rust", envelope)
	if err != nil || !transferred.Allowed || transferred.Reason != "qualified_transfer_envelope" ||
		transferred.TransferEnvelopeID != envelope.EnvelopeID ||
		transferred.PromotionAuthorized || transferred.HostActionAuthorized {
		t.Fatalf("qualified reuse decision: %+v %v", transferred, err)
	}
	other, err := QualifyProcedureReuse("procedure-other", "repair:python", "repair:rust", envelope)
	if err != nil || other.Allowed || other.TransferEnvelopeID != envelope.EnvelopeID {
		t.Fatalf("unqualified memory was admitted: %+v %v", other, err)
	}
	invalid := *envelope
	invalid.VerifiedOutcomeIDs = []string{"credit-a"}
	invalid.IndependenceDomains = []string{"project:um-mem"}
	if _, err := QualifyProcedureReuse("procedure-1", "repair:python", "repair:rust", &invalid); err == nil {
		t.Fatal("accepted transfer with one outcome was accepted")
	}
}
