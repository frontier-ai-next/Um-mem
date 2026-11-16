package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
)

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character != '0' && (character < '1' || character > '9') &&
			(character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func pythonHashValue(value any) (string, error) {
	compact, err := jsoncompat.MarshalPython(value, false)
	if err != nil {
		return "", err
	}
	// hash_value uses json.dumps(sort_keys=True) with Python's default
	// separators, which include one space after every comma and colon.
	var output strings.Builder
	quoted, escaped := false, false
	for _, character := range string(compact) {
		if quoted {
			output.WriteRune(character)
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				quoted = false
			}
			continue
		}
		output.WriteRune(character)
		if character == '"' {
			quoted = true
		} else if character == ',' || character == ':' {
			output.WriteByte(' ')
		}
	}
	digest := sha256.Sum256([]byte(output.String()))
	return hex.EncodeToString(digest[:]), nil
}

// PythonHashValue preserves the canonical hash_value encoding used in
// persisted Python correction proposal keys.
func PythonHashValue(value any) (string, error) { return pythonHashValue(value) }

func exactStringMap(value any, fields []string) (map[string]any, bool) {
	selected, ok := value.(map[string]any)
	if !ok || len(selected) != len(fields) {
		return nil, false
	}
	for _, field := range fields {
		if _, ok := selected[field].(string); !ok {
			return nil, false
		}
	}
	return selected, true
}

func contextEvidenceRefs(source map[string]any) []any {
	return contextSlice(source["evidence_refs"])
}

func boundSemanticReview(source map[string]any) bool {
	fields := []string{"record_id", "revision_id", "content_hash", "source_authority_id",
		"source_authority_hash", "review_authority_id", "review_authority_hash", "manifest_hash"}
	review, ok := exactStringMap(source["semantic_review"], fields)
	if !ok || source["verification_scope"] != "semantic" ||
		source["promotion_source_id"] != review["record_id"] {
		return false
	}
	for _, field := range []string{"record_id", "revision_id", "source_authority_id", "review_authority_id"} {
		value := review[field].(string)
		if strings.TrimSpace(value) == "" || len(value) > 512 || strings.ContainsRune(value, 0) {
			return false
		}
	}
	for _, field := range []string{"content_hash", "source_authority_hash", "review_authority_hash", "manifest_hash"} {
		if !validSHA256(review[field].(string)) {
			return false
		}
	}
	if review["source_authority_id"] == review["review_authority_id"] ||
		review["source_authority_hash"] == review["review_authority_hash"] {
		return false
	}
	for _, raw := range contextEvidenceRefs(source) {
		ref := contextMap(raw)
		if ref["ref_type"] == "verification_report" && ref["producer_id"] == review["review_authority_id"] &&
			contextValue(ref["hash"]) {
			return true
		}
	}
	return false
}

func boundCandidateVerification(source map[string]any) bool {
	verification := contextMap(source["candidate_semantic_verification"])
	if len(verification) != 2 {
		return false
	}
	result := contextMap(verification["result"])
	report := contextMap(verification["report"])
	if len(result) == 0 || len(report) == 0 || report["passed"] != true || !contextValue(report["ledger_hash"]) {
		return false
	}
	for field := range report {
		if !slices.Contains([]string{"report_id", "report_hash", "workspace_hash", "diff_hash",
			"produced_after_event_id", "verifier_id", "passed", "ledger_hash"}, field) {
			return false
		}
	}
	for _, field := range []string{"report_id", "report_hash", "workspace_hash", "diff_hash",
		"produced_after_event_id", "verifier_id", "ledger_hash"} {
		if _, ok := report[field].(string); !ok {
			return false
		}
	}
	subject, ok := exactStringMap(result["subject"], []string{"workspace_id", "profile_id", "memory_id",
		"revision_id", "payload_hash", "source_evidence_hash", "target_block_id", "authority", "scope", "scope_id"})
	if !ok {
		return false
	}
	for _, value := range subject {
		text := value.(string)
		if text == "" || text != strings.TrimSpace(text) {
			return false
		}
	}
	if subject["authority"] != "advisory" && subject["authority"] != "directive" {
		return false
	}
	scopeField := map[string]string{"workspace": "workspace_id", "project": "project_id",
		"user_global": "user_id", "session": "session_id", "task": "task_id", "subtask": "subtask_id"}[subject["scope"].(string)]
	if scopeField == "" || subject["memory_id"] != source["promotion_source_id"] ||
		subject["revision_id"] != source["promotion_source_revision_id"] ||
		subject["workspace_id"] != source["workspace_id"] ||
		subject["profile_id"] != source["profile_id"] ||
		subject["authority"] != source["authority"] || subject["scope"] != source["scope"] ||
		subject["scope_id"] != source[scopeField] ||
		subject["target_block_id"] != contextMap(source["block_membership"])["primary_block_id"] {
		return false
	}
	for _, field := range []string{"verifier_id", "verifier_model_version", "proposer_model_version", "verifier_prompt_version"} {
		value, ok := result[field].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return false
		}
	}
	if result["verifier_id"] != report["verifier_id"] ||
		result["verifier_model_version"] == result["proposer_model_version"] {
		return false
	}
	for _, field := range []string{"coverage", "preservation", "faithfulness", "instruction_safety"} {
		if result[field] != true {
			return false
		}
	}
	reasons := contextSlice(result["reasons"])
	if len(reasons) == 0 {
		return false
	}
	for _, reason := range reasons {
		text, ok := reason.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return false
		}
	}
	resultHash, err := pythonHashValue(result)
	if err != nil || report["report_hash"] != resultHash {
		return false
	}
	subjectHash, err := pythonHashValue(subject)
	if err != nil || result["semantic_subject_hash"] != subjectHash {
		return false
	}
	for _, raw := range contextEvidenceRefs(source) {
		ref := contextMap(raw)
		if ref["ref_type"] == "verification_report" && ref["ref_id"] == report["report_id"] &&
			ref["producer_id"] == report["verifier_id"] && ref["hash"] == report["ledger_hash"] {
			return true
		}
	}
	return false
}
