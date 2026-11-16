package ledger

import (
	"errors"
	"reflect"
	"slices"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func exactGateIdentity(value string) bool { return value != "" && value == strings.TrimSpace(value) }

func applicationEventResult(row map[string]any) (map[string]any, error) {
	if row["actor"] != "verifier" || row["tool"] != "memory_application_verification" {
		return nil, nil
	}
	eventHash, ok := row["event_hash"].(string)
	if !ok || eventHash == "" {
		return nil, errors.New("application verifier event has no hash")
	}
	unsigned := make(map[string]any, len(row)-1)
	for key, value := range row {
		if key != "event_hash" {
			unsigned[key] = value
		}
	}
	computedEventHash, err := HashValue(unsigned)
	if err != nil || computedEventHash != eventHash {
		return nil, errors.New("application verifier event hash is invalid")
	}
	result, ok := row["result"].(map[string]any)
	if !ok {
		return nil, errors.New("application verifier result is invalid")
	}
	computedResultHash, err := HashValue(result)
	if err != nil || row["result_hash"] != computedResultHash {
		return nil, errors.New("application verifier result hash is invalid")
	}
	return result, nil
}

// VerifiedProcedurePromotionGate counts independently applied, ledger-backed
// helpful outcomes. A verified harmful application blocks promotion entirely.
func (resolver *Resolver) VerifiedProcedurePromotionGate(memoryID, profileID,
	taskSignature string, credits []memory.OutcomeCreditEvent) (memory.ProcedurePromotionGate, error) {
	if !exactGateIdentity(memoryID) || !exactGateIdentity(profileID) || !exactGateIdentity(taskSignature) {
		return memory.ProcedurePromotionGate{}, errors.New("procedure gate requires exact memory, profile and task identities")
	}
	// A backend can omit or rewrite a credit and recompute its open SHA-256
	// envelope. Host-owned verifier events therefore veto verified harm even
	// when no matching SQLite credit is returned.
	for _, row := range resolver.rows {
		result, err := applicationEventResult(row)
		if err != nil {
			return memory.ProcedurePromotionGate{}, err
		}
		if result != nil && row["workspace_id"] == resolver.workspaceID &&
			result["memory_id"] == memoryID && result["outcome"] == "hurt" &&
			result["passed"] == true {
			return memory.ProcedurePromotionGate{}, errors.New("procedure has a verified harmful application")
		}
	}
	outcomeIDs := make([]string, 0)
	domains := make([]string, 0)
	decisions := make(map[string]bool)
	traces := make(map[string]bool)
	reports := make(map[string]bool)
	for _, credit := range credits {
		if err := credit.Validate(); err != nil {
			return memory.ProcedurePromotionGate{}, err
		}
		if !slices.Contains(credit.MemoryIDs, memoryID) || credit.ProfileID != profileID ||
			credit.Outcome == "neutral" {
			continue
		}
		verifiedReports := make([]string, 0)
		creditSubject, err := credit.ApplicationSubject()
		if err != nil {
			return memory.ProcedurePromotionGate{}, err
		}
		creditSubjectHash, err := HashValue(creditSubject)
		if err != nil {
			return memory.ProcedurePromotionGate{}, err
		}
		for _, ref := range credit.EvidenceRefs {
			if ref.RefType != "verification_report" || ref.ProducedBy != "verifier" ||
				len(resolver.ValidateRef(ref)) != 0 {
				continue
			}
			row := resolver.Row(ref.RefID)
			if row == nil || row["workspace_id"] != credit.WorkspaceID ||
				row["actor"] != "verifier" || row["tool"] != "memory_application_verification" {
				continue
			}
			result, err := applicationEventResult(row)
			if err != nil {
				return memory.ProcedurePromotionGate{}, err
			}
			if result == nil {
				continue
			}
			usageID, usageOK := result["usage_event_id"].(string)
			if result["passed"] != true || result["verifier_id"] != ref.ProducerID ||
				result["decision_id"] != credit.DecisionID ||
				result["action_trace_id"] != credit.ActionTraceID ||
				result["memory_id"] != memoryID ||
				!usageOK || !slices.Contains(credit.AppliedUsageEventIDs, usageID) ||
				result["task_signature"] != credit.TaskSignature ||
				result["outcome"] != credit.Outcome ||
				result["independence_domain"] != credit.IndependenceDomain ||
				result["profile_id"] != credit.ProfileID || result["credit_id"] != credit.CreditID ||
				result["credit_subject_hash"] != creditSubjectHash ||
				!reflect.DeepEqual(result["credit_subject"], creditSubject) {
				continue
			}
			verifiedReports = append(verifiedReports, ref.RefID)
		}
		if credit.Outcome == "hurt" && len(verifiedReports) > 0 {
			return memory.ProcedurePromotionGate{}, errors.New("procedure has a verified harmful application")
		}
		if credit.Outcome != "helped" || credit.TaskSignature != taskSignature ||
			len(credit.MemoryIDs) != 1 || credit.MemoryIDs[0] != memoryID ||
			len(credit.AppliedUsageEventIDs) != 1 ||
			credit.AttributionBasis != "deterministic_verifier" ||
			credit.VerifierResult != "passed" {
			continue
		}
		duplicateReport := false
		for _, reportID := range verifiedReports {
			duplicateReport = duplicateReport || reports[reportID]
		}
		if len(verifiedReports) == 0 || decisions[credit.DecisionID] ||
			traces[credit.ActionTraceID] || slices.Contains(domains, credit.IndependenceDomain) ||
			duplicateReport {
			continue
		}
		outcomeIDs = append(outcomeIDs, credit.CreditID)
		domains = append(domains, credit.IndependenceDomain)
		decisions[credit.DecisionID] = true
		traces[credit.ActionTraceID] = true
		for _, reportID := range verifiedReports {
			reports[reportID] = true
		}
	}
	return memory.NewProcedurePromotionGate(outcomeIDs, domains, 2)
}
