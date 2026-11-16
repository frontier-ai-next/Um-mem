package memory

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// TransferEnvelope records the evidence and scope boundaries for a proposed
// cross-task procedure transfer. An accepted envelope is still advisory.
type TransferEnvelope struct {
	EnvelopeID          string        `json:"envelope_id"`
	EvidenceRefs        []EvidenceRef `json:"evidence_refs"`
	Status              string        `json:"status"`
	SourceScope         string        `json:"source_scope"`
	TargetScope         string        `json:"target_scope"`
	SourceTaskSignature string        `json:"source_task_signature"`
	TargetTaskSignature string        `json:"target_task_signature"`
	QualifiedMemoryIDs  []string      `json:"qualified_memory_ids"`
	VerifiedOutcomeIDs  []string      `json:"verified_outcome_ids"`
	IndependenceDomains []string      `json:"independence_domains"`
	SourceWorkflow      string        `json:"source_workflow"`
	TargetWorkflow      string        `json:"target_workflow"`
	SourceProjectID     string        `json:"source_project_id"`
	TargetProjectID     string        `json:"target_project_id"`
	SourceHost          string        `json:"source_host"`
	TargetHost          string        `json:"target_host"`
	SourceProfile       string        `json:"source_profile"`
	TargetProfile       string        `json:"target_profile"`
	SourceStages        []string      `json:"source_stages"`
	TargetStages        []string      `json:"target_stages"`
	Prerequisites       []string      `json:"prerequisites"`
	Contraindications   []string      `json:"contraindications"`
	Exclusions          []string      `json:"exclusions"`
	SupportCount        int           `json:"support_count"`
	SourceRevisionID    string        `json:"source_revision_id"`
	ValidFrom           string        `json:"valid_from"`
	ExpiresAt           string        `json:"expires_at"`
	DriftFingerprint    string        `json:"drift_fingerprint"`
}

func validateTransferTexts(name string, values []string) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len([]rune(value)) > 256 {
			return fmt.Errorf("%s must contain nonempty terms of at most 256 characters", name)
		}
	}
	return nil
}

func requiredTransferText(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must be a non-empty string", name)
	}
	return nil
}

// Validate applies the accepted-transfer evidence gate before reuse decisions.
func (envelope TransferEnvelope) Validate() error {
	if err := requiredTransferText("envelope_id", envelope.EnvelopeID); err != nil {
		return err
	}
	status := envelope.Status
	if status == "" {
		status = "pending"
	}
	if status != "pending" && status != "accepted" && status != "rejected" {
		return errors.New("transfer status must be pending, accepted, or rejected")
	}
	for _, ref := range envelope.EvidenceRefs {
		if err := ref.Validate(); err != nil {
			return err
		}
	}
	for _, field := range []struct {
		name   string
		values []string
	}{{"qualified_memory_ids", envelope.QualifiedMemoryIDs},
		{"verified_outcome_ids", envelope.VerifiedOutcomeIDs},
		{"independence_domains", envelope.IndependenceDomains},
		{"source_stages", envelope.SourceStages}, {"target_stages", envelope.TargetStages},
		{"prerequisites", envelope.Prerequisites},
		{"contraindications", envelope.Contraindications}, {"exclusions", envelope.Exclusions}} {
		if err := validateTransferTexts(field.name, field.values); err != nil {
			return err
		}
	}
	if status != "accepted" {
		return nil
	}
	for _, field := range []struct{ name, value string }{
		{"accepted transfer source_scope", envelope.SourceScope},
		{"accepted transfer target_scope", envelope.TargetScope},
		{"accepted transfer source_task_signature", envelope.SourceTaskSignature},
		{"accepted transfer target_task_signature", envelope.TargetTaskSignature},
		{"accepted transfer source_workflow", envelope.SourceWorkflow},
		{"accepted transfer target_workflow", envelope.TargetWorkflow},
		{"accepted transfer source_project_id", envelope.SourceProjectID},
		{"accepted transfer target_project_id", envelope.TargetProjectID},
		{"accepted transfer source_host", envelope.SourceHost},
		{"accepted transfer target_host", envelope.TargetHost},
		{"accepted transfer source_profile", envelope.SourceProfile},
		{"accepted transfer target_profile", envelope.TargetProfile},
		{"accepted transfer source_revision_id", envelope.SourceRevisionID},
		{"accepted transfer drift_fingerprint", envelope.DriftFingerprint}} {
		if err := requiredTransferText(field.name, field.value); err != nil {
			return err
		}
	}
	if envelope.SourceScope == envelope.TargetScope ||
		envelope.SourceTaskSignature == envelope.TargetTaskSignature ||
		envelope.SourceWorkflow == envelope.TargetWorkflow ||
		envelope.SourceProjectID == envelope.TargetProjectID {
		return errors.New("accepted transfer requires distinct source and target scope, task, workflow, and project")
	}
	if len(envelope.EvidenceRefs) == 0 || len(envelope.QualifiedMemoryIDs) == 0 ||
		len(envelope.SourceStages) == 0 || len(envelope.TargetStages) == 0 {
		return errors.New("accepted transfer requires evidence, qualified memories, and source and target stages")
	}
	if envelope.SupportCount < 2 || envelope.SupportCount < len(envelope.VerifiedOutcomeIDs) {
		return errors.New("accepted transfer requires declared independent support count")
	}
	from, err := time.Parse(time.RFC3339Nano, envelope.ValidFrom)
	if err != nil {
		return errors.New("accepted transfer valid_from must include an ISO-8601 timezone")
	}
	until, err := time.Parse(time.RFC3339Nano, envelope.ExpiresAt)
	if err != nil || !from.Before(until) {
		return errors.New("accepted transfer expiry must follow valid_from")
	}
	gate, err := NewProcedurePromotionGate(envelope.VerifiedOutcomeIDs, envelope.IndependenceDomains, 2)
	if err != nil {
		return err
	}
	if !gate.Allowed {
		return errors.New("accepted transfer requires multiple independently verified outcomes")
	}
	for _, domain := range gate.IndependenceDomains {
		if !strings.HasPrefix(domain, "project:") {
			return errors.New("accepted transfer requires independent project evidence domains")
		}
	}
	return nil
}

// ProcedureReuseDecision never authorizes promotion or a host action.
type ProcedureReuseDecision struct {
	MemoryID               string `json:"memory_id"`
	SourceTaskSignature    string `json:"source_task_signature"`
	RequestedTaskSignature string `json:"requested_task_signature"`
	Allowed                bool   `json:"allowed"`
	Reason                 string `json:"reason"`
	TransferEnvelopeID     string `json:"transfer_envelope_id"`
	PromotionAuthorized    bool   `json:"promotion_authorized"`
	HostActionAuthorized   bool   `json:"host_action_authorized"`
}

// QualifyProcedureReuse admits the original task signature or a structurally
// accepted transfer that names this exact memory and target signature.
func QualifyProcedureReuse(memoryID, sourceTaskSignature, requestedTaskSignature string,
	envelope *TransferEnvelope) (ProcedureReuseDecision, error) {
	for _, field := range []struct{ name, value string }{{"memory_id", memoryID},
		{"source_task_signature", sourceTaskSignature},
		{"requested_task_signature", requestedTaskSignature}} {
		if err := requiredTransferText(field.name, field.value); err != nil {
			return ProcedureReuseDecision{}, err
		}
	}
	decision := ProcedureReuseDecision{MemoryID: strings.TrimSpace(memoryID),
		SourceTaskSignature:    strings.TrimSpace(sourceTaskSignature),
		RequestedTaskSignature: strings.TrimSpace(requestedTaskSignature)}
	if decision.SourceTaskSignature == decision.RequestedTaskSignature {
		decision.Allowed, decision.Reason = true, "same_task_signature"
		return decision, nil
	}
	if envelope != nil {
		if err := envelope.Validate(); err != nil {
			return ProcedureReuseDecision{}, err
		}
		decision.TransferEnvelopeID = strings.TrimSpace(envelope.EnvelopeID)
		if envelope.Status == "accepted" && envelope.SourceTaskSignature == decision.SourceTaskSignature &&
			envelope.TargetTaskSignature == decision.RequestedTaskSignature {
			for _, qualified := range envelope.QualifiedMemoryIDs {
				if qualified == decision.MemoryID {
					decision.Allowed, decision.Reason = true, "qualified_transfer_envelope"
					return decision, nil
				}
			}
		}
	}
	decision.Reason = "cross_task_signature_requires_qualified_transfer"
	return decision, nil
}
