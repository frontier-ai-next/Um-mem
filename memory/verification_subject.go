package memory

import (
	"errors"
	"strings"
)

// CandidateVerificationSubject binds a verifier report to the exact source
// revision, evidence, destination and authority before publication.
type CandidateVerificationSubject struct {
	WorkspaceID        string `json:"workspace_id"`
	ProfileID          string `json:"profile_id"`
	MemoryID           string `json:"memory_id"`
	RevisionID         string `json:"revision_id"`
	PayloadHash        string `json:"payload_hash"`
	SourceEvidenceHash string `json:"source_evidence_hash"`
	TargetBlockID      string `json:"target_block_id"`
	Authority          string `json:"authority"`
	Scope              string `json:"scope"`
	ScopeID            string `json:"scope_id"`
}

func (subject CandidateVerificationSubject) Validate() error {
	for _, value := range []string{subject.WorkspaceID, subject.ProfileID,
		subject.MemoryID, subject.RevisionID, subject.PayloadHash,
		subject.SourceEvidenceHash, subject.TargetBlockID, subject.Authority,
		subject.Scope, subject.ScopeID} {
		if value == "" || value != strings.TrimSpace(value) {
			return errors.New("verification subject contains an unbound identity")
		}
	}
	if subject.Authority != "advisory" && subject.Authority != "directive" {
		return errors.New("verification subject authority is unsupported")
	}
	return nil
}
