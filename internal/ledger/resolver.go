package ledger

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// Resolver indexes one workspace's supervisor ledger for evidence binding.
// Malformed lines are ignored like the Python reader; duplicate event IDs or
// hashes are rejected because they make a proof ambiguous.
type Resolver struct {
	workspaceID string
	rows        []map[string]any
	byID        map[string]map[string]any
	positions   map[string]int
}

func OpenResolver(repoRoot, workspaceID string) (*Resolver, error) {
	path, err := Path(repoRoot, workspaceID)
	if err != nil {
		return nil, err
	}
	result := &Resolver{workspaceID: workspaceID, rows: []map[string]any{}, byID: map[string]map[string]any{},
		positions: map[string]int{}}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil || row == nil {
			continue
		}
		position := len(result.rows)
		for _, key := range []string{"event_id", "event_hash"} {
			identity, _ := row[key].(string)
			if identity == "" {
				continue
			}
			if result.byID[identity] != nil {
				return nil, errors.New("supervisor ledger contains duplicate event identities")
			}
			result.byID[identity] = row
			result.positions[identity] = position
		}
		result.rows = append(result.rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (resolver *Resolver) Row(identity string) map[string]any {
	if resolver == nil {
		return nil
	}
	row := resolver.byID[identity]
	if row == nil {
		return nil
	}
	// Python's ledger_row returns a deepcopy. A caller must not be able to
	// rewrite the resolver's cached proof before a later validation.
	encoded, err := json.Marshal(row)
	if err != nil {
		return nil
	}
	var copy map[string]any
	if err := json.Unmarshal(encoded, &copy); err != nil {
		return nil
	}
	return copy
}

// ValidateRef returns blocking Python contract issue codes for a ledger-backed
// reference. Non-ledger references retain their typed validation boundary.
func (resolver *Resolver) ValidateRef(ref memory.EvidenceRef) []string {
	if err := ref.Validate(); err != nil {
		return []string{"invalid_evidence_ref"}
	}
	if ref.RefType != "ledger_event" && ref.RefType != "verification_report" && ref.RefType != "test_run" {
		return nil
	}
	row := resolver.Row(ref.RefID)
	if row == nil {
		return []string{"fake_evidence_ref"}
	}
	issues := make([]string, 0)
	hash, _ := row["event_hash"].(string)
	if ref.Hash != nil && *ref.Hash != "" && *ref.Hash != hash {
		issues = append(issues, "evidence_hash_mismatch")
	}
	if ref.ProducedBy != "agent" && ref.ProducedBy != row["actor"] {
		issues = append(issues, "wrong_evidence_producer")
	}
	if ref.CreatedAfterEvent != nil && *ref.CreatedAfterEvent != "" {
		after, ok := resolver.positions[*ref.CreatedAfterEvent]
		if !ok {
			issues = append(issues, "fake_evidence_ref")
		} else if resolver.positions[ref.RefID] <= after {
			issues = append(issues, "stale_proof_ref")
		}
	}
	return issues
}

// ValidateVerificationReport binds a typed report to its supervisor event and
// current workspace bytes. The returned codes mirror blocking Python issues.
func (resolver *Resolver) ValidateVerificationReport(ref VerificationReport,
	workspaceRoot string, changedFiles []string, allowWorkspaceHashMismatch bool) ([]string, error) {
	hash := ref.LedgerHash
	var after *string
	if ref.ProducedAfterEventID != "" {
		after = &ref.ProducedAfterEventID
	}
	issues := resolver.ValidateRef(memory.EvidenceRef{RefType: "verification_report",
		RefID: ref.ReportID, Hash: &hash, ProducedBy: "verifier",
		ProducerID: ref.VerifierID, CreatedAfterEvent: after})
	row := resolver.Row(ref.ReportID)
	if row == nil {
		return issues, nil
	}
	result, ok := row["result"].(map[string]any)
	if !ok || result["verifier_id"] != ref.VerifierID {
		issues = append(issues, "verification_report_verifier_mismatch")
	}
	expectedHash, err := HashValue(map[string]any{"report_hash": ref.ReportHash,
		"passed": ref.Passed, "workspace_hash": ref.WorkspaceHash,
		"diff_hash": ref.DiffHash})
	if err != nil {
		return nil, err
	}
	if row["result_hash"] != expectedHash {
		issues = append(issues, "verification_report_hash_mismatch")
	}
	currentWorkspaceHash := ""
	if !allowWorkspaceHashMismatch {
		currentWorkspaceHash, err = WorkspaceHash(workspaceRoot)
		if err != nil {
			return nil, err
		}
	}
	currentDiffHash, err := DiffHash(workspaceRoot, changedFiles)
	if err != nil {
		return nil, err
	}
	scopedDiffMatches := allowWorkspaceHashMismatch && currentDiffHash != "" &&
		ref.DiffHash != "" && ref.DiffHash == currentDiffHash
	if ref.WorkspaceHash != "" && ref.WorkspaceHash != currentWorkspaceHash && !scopedDiffMatches {
		issues = append(issues, "proof_stale_rerun_required")
	}
	workspaceMatches := ref.WorkspaceHash != "" && ref.WorkspaceHash == currentWorkspaceHash
	if !workspaceMatches && currentDiffHash != "" && ref.DiffHash != "" &&
		ref.DiffHash != currentDiffHash && ref.DiffHash != currentWorkspaceHash {
		issues = append(issues, "proof_stale_rerun_required")
	}
	if !ref.Passed {
		issues = append(issues, "verification_report_not_passed")
	}
	return issues, nil
}
