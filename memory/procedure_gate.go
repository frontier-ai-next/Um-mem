package memory

import (
	"errors"
	"strings"
	"unicode/utf8"
)

type ProcedurePromotionGate struct {
	VerifiedOutcomeIDs      []string `json:"verified_outcome_ids"`
	IndependenceDomains     []string `json:"independence_domains"`
	MinimumVerifiedOutcomes int      `json:"minimum_verified_outcomes"`
	Allowed                 bool     `json:"allowed"`
	Reason                  string   `json:"reason"`
}

func normalizeGateIdentities(values []string) ([]string, error) {
	unique := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" || utf8.RuneCountInString(value) > 256 {
			return nil, errors.New("procedure gate identities must be nonempty and at most 256 characters")
		}
		if !seen[value] {
			unique = append(unique, value)
			seen[value] = true
		}
	}
	return unique, nil
}

func NewProcedurePromotionGate(outcomeIDs, domains []string, minimum int) (ProcedurePromotionGate, error) {
	if minimum < 2 {
		return ProcedurePromotionGate{}, errors.New("minimum_verified_outcomes must be at least two")
	}
	verified, err := normalizeGateIdentities(outcomeIDs)
	if err != nil {
		return ProcedurePromotionGate{}, err
	}
	independent, err := normalizeGateIdentities(domains)
	if err != nil {
		return ProcedurePromotionGate{}, err
	}
	allowed := len(verified) >= minimum && len(independent) >= minimum
	reason := "insufficient_independent_verified_outcomes"
	if allowed {
		reason = "independent_verified_outcomes_satisfied"
	}
	return ProcedurePromotionGate{VerifiedOutcomeIDs: verified,
		IndependenceDomains: independent, MinimumVerifiedOutcomes: minimum,
		Allowed: allowed, Reason: reason}, nil
}
