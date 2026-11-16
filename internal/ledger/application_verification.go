package ledger

import (
	"errors"
	"slices"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

type ApplicationVerificationRequest struct {
	RepoRoot     string
	VerifierID   string
	Credit       memory.OutcomeCreditEvent
	MemoryID     string
	UsageEventID string
	Passed       bool
}

// AppendMemoryApplicationVerification binds one application to every credit
// field that can affect procedure authority. The verifier ref can then be
// added to Credit.EvidenceRefs without changing this subject hash.
func AppendMemoryApplicationVerification(request ApplicationVerificationRequest) (memory.EvidenceRef, error) {
	if strings.TrimSpace(request.RepoRoot) == "" {
		return memory.EvidenceRef{}, errors.New("application verification requires repo root")
	}
	if err := request.Credit.Validate(); err != nil {
		return memory.EvidenceRef{}, err
	}
	for _, value := range []string{request.VerifierID, request.MemoryID, request.UsageEventID} {
		if !exactGateIdentity(value) {
			return memory.EvidenceRef{}, errors.New("application verification requires exact identities")
		}
	}
	if !slices.Contains(request.Credit.MemoryIDs, request.MemoryID) ||
		!slices.Contains(request.Credit.AppliedUsageEventIDs, request.UsageEventID) {
		return memory.EvidenceRef{}, errors.New("application verification must name a credit-linked application")
	}
	if request.Credit.Outcome != "helped" && request.Credit.Outcome != "hurt" {
		return memory.EvidenceRef{}, errors.New("application verification outcome must be helped or hurt")
	}
	subject, err := request.Credit.ApplicationSubject()
	if err != nil {
		return memory.EvidenceRef{}, err
	}
	subjectHash, err := HashValue(subject)
	if err != nil {
		return memory.EvidenceRef{}, err
	}
	result := map[string]any{"verifier_id": request.VerifierID,
		"decision_id": request.Credit.DecisionID, "action_trace_id": request.Credit.ActionTraceID,
		"memory_id": request.MemoryID, "usage_event_id": request.UsageEventID,
		"task_signature":      request.Credit.TaskSignature,
		"independence_domain": request.Credit.IndependenceDomain,
		"profile_id":          request.Credit.ProfileID, "credit_id": request.Credit.CreditID,
		"credit_subject": subject, "credit_subject_hash": subjectHash,
		"outcome": request.Credit.Outcome, "passed": request.Passed}
	event, err := Append(request.RepoRoot, request.Credit.WorkspaceID, "verifier",
		"memory_application", "memory_application_verification", result)
	if err != nil {
		return memory.EvidenceRef{}, err
	}
	return memory.EvidenceRef{RefType: "verification_report", RefID: event.ID,
		Hash: &event.Hash, ProducedBy: "verifier", ProducerID: request.VerifierID}, nil
}
