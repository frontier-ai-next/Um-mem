package memory

import "errors"

// ConsolidationInputGateResult keeps non-episodic inputs in shadow mode until
// their transitive source preservation has been qualified.
type ConsolidationInputGateResult struct {
	InputMode             string `json:"input_mode"`
	PreservationQualified bool   `json:"preservation_qualified"`
	ConsolidationAllowed  bool   `json:"consolidation_allowed"`
	ShadowOnly            bool   `json:"shadow_only"`
	FallbackAction        string `json:"fallback_action"`
	Reason                string `json:"reason"`
}

func EvaluateConsolidationInput(inputMode string, preservationQualified bool) (ConsolidationInputGateResult, error) {
	if inputMode != "episodic_only" && inputMode != "heterogeneous" && inputMode != "streamed" {
		return ConsolidationInputGateResult{}, errors.New("consolidation input mode is invalid")
	}
	allowed := inputMode == "episodic_only" || preservationQualified
	result := ConsolidationInputGateResult{InputMode: inputMode,
		PreservationQualified: preservationQualified, ConsolidationAllowed: allowed,
		ShadowOnly: !allowed, FallbackAction: "retain",
		Reason: "heterogeneous_or_streamed_preservation_not_qualified"}
	if allowed {
		result.FallbackAction = "defer"
		result.Reason = "episodic_or_preservation_qualified"
	}
	return result, nil
}
