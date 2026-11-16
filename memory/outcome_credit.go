package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// OutcomeCreditEvent is the durable Python-compatible application outcome
// carried in the SQLite usage journal.
type OutcomeCreditEvent struct {
	CreditID             string        `json:"credit_id"`
	DecisionID           string        `json:"decision_id"`
	WorkspaceID          string        `json:"workspace_id"`
	TaskSignature        string        `json:"task_signature"`
	ActionTraceID        string        `json:"action_trace_id"`
	MemoryIDs            []string      `json:"memory_ids"`
	AppliedUsageEventIDs []string      `json:"applied_usage_event_ids"`
	Outcome              string        `json:"outcome"`
	AttributionBasis     string        `json:"attribution_basis"`
	VerifierResult       string        `json:"verifier_result"`
	ApprovalPrincipalID  string        `json:"approval_principal_id"`
	TaskFamily           string        `json:"task_family"`
	Stage                string        `json:"stage"`
	ProjectID            string        `json:"project_id"`
	HostID               string        `json:"host_id"`
	ProfileID            string        `json:"profile_id"`
	IndependenceDomain   string        `json:"independence_domain"`
	EvidenceRefs         []EvidenceRef `json:"evidence_refs"`
	Reason               string        `json:"reason"`
	CreatedAt            string        `json:"created_at"`
}

// ApplicationSubject excludes only evidence_refs, because the verifier event
// itself becomes one of those refs after it is appended. All credit fields
// that can affect procedure authority remain bound to the host proof.
func (event OutcomeCreditEvent) ApplicationSubject() (map[string]any, error) {
	encoded, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	var subject map[string]any
	if err := json.Unmarshal(encoded, &subject); err != nil {
		return nil, err
	}
	delete(subject, "evidence_refs")
	return subject, nil
}

func uniqueOutcomeIdentities(values []string) error {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) || utf8.RuneCountInString(value) > 256 || seen[value] {
			return errors.New("outcome credit identities must be unique exact strings of at most 256 characters")
		}
		seen[value] = true
	}
	return nil
}

func (event OutcomeCreditEvent) Validate() error {
	for _, value := range []string{event.CreditID, event.DecisionID, event.WorkspaceID,
		event.TaskSignature, event.ActionTraceID, event.TaskFamily, event.Stage,
		event.ProjectID, event.HostID, event.ProfileID, event.IndependenceDomain,
		event.Reason} {
		if value == "" || value != strings.TrimSpace(value) {
			return errors.New("outcome credit requires exact nonempty identities")
		}
	}
	if err := uniqueOutcomeIdentities(event.MemoryIDs); err != nil {
		return err
	}
	if err := uniqueOutcomeIdentities(event.AppliedUsageEventIDs); err != nil {
		return err
	}
	if len(event.EvidenceRefs) == 0 {
		return errors.New("outcome credit requires evidence refs")
	}
	for _, ref := range event.EvidenceRefs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("outcome credit evidence: %w", err)
		}
	}
	if event.VerifierResult != "" && event.VerifierResult != "passed" &&
		event.VerifierResult != "failed" && event.VerifierResult != "inconclusive" {
		return errors.New("outcome credit verifier result is unsupported")
	}
	if event.Outcome != "helped" && event.Outcome != "hurt" && event.Outcome != "neutral" {
		return errors.New("outcome credit outcome is unsupported")
	}
	if event.Outcome == "helped" || event.Outcome == "hurt" {
		if len(event.MemoryIDs) == 0 || len(event.AppliedUsageEventIDs) == 0 {
			return errors.New("applied outcome credit requires memory and usage linkage")
		}
	}
	switch event.Outcome {
	case "helped":
		if event.AttributionBasis == "deterministic_verifier" {
			if event.VerifierResult != "passed" || !outcomeEvidence(event.EvidenceRefs, "test_run", "verification_report") {
				return errors.New("helped credit requires passed deterministic verifier evidence")
			}
		} else if event.AttributionBasis == "trusted_approval" {
			if event.ApprovalPrincipalID == "" || !outcomeEvidence(event.EvidenceRefs, "human_approval") {
				return errors.New("helped credit requires trusted approval evidence")
			}
		} else {
			return errors.New("helped credit requires verifier or trusted approval basis")
		}
	case "hurt":
		if event.AttributionBasis != "proven_stale_application" &&
			event.AttributionBasis != "proven_incorrect_application" &&
			event.AttributionBasis != "proven_contamination" &&
			event.AttributionBasis != "proven_regression" {
			return errors.New("hurt credit requires a proven harm basis")
		}
	case "neutral":
		if event.AttributionBasis != "insufficient_linkage" {
			return errors.New("neutral credit requires insufficient linkage")
		}
	}
	if event.CreatedAt != "" {
		_, err := time.Parse(time.RFC3339Nano, event.CreatedAt)
		if err != nil {
			_, err = time.Parse("2006-01-02 15:04:05.999999999Z07:00", event.CreatedAt)
		}
		if err != nil {
			return errors.New("outcome credit created_at requires a timezone")
		}
	}
	return nil
}

func outcomeEvidence(refs []EvidenceRef, types ...string) bool {
	for _, ref := range refs {
		if ref.ProducerID == "" {
			continue
		}
		for _, kind := range types {
			if ref.RefType == kind {
				return true
			}
		}
	}
	return false
}
