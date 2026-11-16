package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
)

var consolidationTriggers = map[string]bool{
	"recurrence": true, "novelty_surprise": true, "correction_conflict": true,
	"explicit_decision_preference": true, "high_risk_authority": true,
	"rare_irreversible_fact": true, "subgoal_boundary": true,
	"task_boundary": true, "session_boundary": true, "verified_reuse_harm": true,
	"capacity_pressure": true, "source_freshness": true,
	"prospective_deadline": true, "session_end": true, "task_end": true,
	"token_pressure": true, "duplicate_density": true, "source_change": true,
	"unresolved_conflict": true, "repeated_verified_reuse": true,
}

var consolidationActions = map[string]bool{
	"retain": true, "defer": true, "split": true, "consolidate": true, "quarantine": true,
}

var consolidationRiskPriority = map[string]int{
	"low": 1, "medium": 2, "high": 3, "critical": 4,
}

// ConsolidationSignal describes a proposal to consider. It never authorizes
// a canonical write or a host action.
type ConsolidationSignal struct {
	WorkspaceID           string        `json:"workspace_id"`
	SourceEpisodeIDs      []string      `json:"source_episode_ids"`
	GroupingKey           string        `json:"grouping_key"`
	TaskFamily            string        `json:"task_family"`
	Trigger               string        `json:"trigger"`
	ProposedAction        string        `json:"proposed_action"`
	ExpectedApplicability string        `json:"expected_applicability"`
	Risk                  string        `json:"risk"`
	CostBudgetTokens      int           `json:"cost_budget_tokens"`
	Rationale             string        `json:"rationale"`
	EvidenceRefs          []EvidenceRef `json:"evidence_refs"`
	ClusterKey            string        `json:"cluster_key"`
	RareCritical          bool          `json:"rare_critical"`
	ExpectedValue         float64       `json:"expected_value"`
	AgeSeconds            int           `json:"age_seconds"`
	EstimatedCostTokens   int           `json:"estimated_cost_tokens"`
	DependencyIDs         []string      `json:"dependency_ids"`
	DependenciesClosed    bool          `json:"dependencies_closed"`
	RecurrenceCount       int           `json:"recurrence_count"`
}

type ConsolidationDecision struct {
	DecisionID            string        `json:"decision_id"`
	WorkspaceID           string        `json:"workspace_id"`
	SourceEpisodeIDs      []string      `json:"source_episode_ids"`
	GroupingKey           string        `json:"grouping_key"`
	TaskFamily            string        `json:"task_family"`
	Trigger               string        `json:"trigger"`
	ExpectedApplicability string        `json:"expected_applicability"`
	Risk                  string        `json:"risk"`
	CostBudgetTokens      int           `json:"cost_budget_tokens"`
	Rationale             string        `json:"rationale"`
	EvidenceRefs          []EvidenceRef `json:"evidence_refs"`
	Action                string        `json:"action"`
	Status                string        `json:"status"`
	ForcedRewrite         bool          `json:"forced_rewrite"`
	HostActionAuthorized  bool          `json:"host_action_authorized"`
}

type ConsolidationScheduleEntry struct {
	Signal            ConsolidationSignal   `json:"signal"`
	Decision          ConsolidationDecision `json:"decision"`
	Order             int                   `json:"order"`
	PriorityScore     float64               `json:"priority_score"`
	Disposition       string                `json:"disposition"`
	Reason            string                `json:"reason"`
	signalFingerprint string
}

type ConsolidationSchedule struct {
	ScheduleID                string                       `json:"schedule_id"`
	Window                    string                       `json:"window"`
	Entries                   []ConsolidationScheduleEntry `json:"entries"`
	GroupingKeys              []string                     `json:"grouping_keys"`
	ClusterKeys               []string                     `json:"cluster_keys"`
	InputSetFingerprintSHA256 string                       `json:"input_set_fingerprint_sha256"`
	CanonicalMutationApplied  bool                         `json:"canonical_mutation_applied"`
	HostActionAuthorized      bool                         `json:"host_action_authorized"`
}

func sortedSignalIDs(values []string, required bool) ([]string, error) {
	if required && len(values) == 0 {
		return nil, errors.New("consolidation source IDs are required")
	}
	result := append([]string{}, values...)
	for i, value := range result {
		result[i] = strings.TrimSpace(value)
		if result[i] == "" {
			return nil, errors.New("consolidation ID must be nonempty")
		}
	}
	sort.Strings(result)
	for i := 1; i < len(result); i++ {
		if result[i] == result[i-1] {
			return nil, errors.New("consolidation IDs must be unique")
		}
	}
	return result, nil
}

func normalizeConsolidationSignal(signal ConsolidationSignal) (ConsolidationSignal, error) {
	fields := []*string{&signal.WorkspaceID, &signal.GroupingKey, &signal.TaskFamily,
		&signal.ExpectedApplicability, &signal.Rationale}
	for _, field := range fields {
		value := strings.TrimSpace(*field)
		if strings.TrimSpace(value) == "" {
			return signal, errors.New("consolidation signal requires workspace, grouping, task family, applicability and rationale")
		}
		*field = value
	}
	if !consolidationTriggers[signal.Trigger] || !consolidationActions[signal.ProposedAction] ||
		consolidationRiskPriority[signal.Risk] == 0 {
		return signal, errors.New("consolidation signal has an unknown trigger, action or risk")
	}
	var err error
	signal.SourceEpisodeIDs, err = sortedSignalIDs(signal.SourceEpisodeIDs, true)
	if err != nil {
		return signal, err
	}
	signal.DependencyIDs, err = sortedSignalIDs(signal.DependencyIDs, false)
	if err != nil {
		return signal, err
	}
	if signal.ClusterKey == "" {
		signal.ClusterKey = signal.GroupingKey
	} else {
		signal.ClusterKey = strings.TrimSpace(signal.ClusterKey)
		if signal.ClusterKey == "" {
			return signal, errors.New("consolidation cluster key is required")
		}
	}
	if signal.CostBudgetTokens < 1 || signal.EstimatedCostTokens < 1 ||
		signal.EstimatedCostTokens > signal.CostBudgetTokens || signal.AgeSeconds < 0 ||
		signal.RecurrenceCount < 1 || math.IsNaN(signal.ExpectedValue) ||
		math.IsInf(signal.ExpectedValue, 0) || signal.ExpectedValue < 0 || signal.ExpectedValue > 1 {
		return signal, errors.New("consolidation signal has invalid cost, age, recurrence or expected value")
	}
	if len(signal.EvidenceRefs) == 0 {
		return signal, errors.New("consolidation signal requires evidence")
	}
	refs := make([]EvidenceRef, 0, len(signal.EvidenceRefs))
	seen := map[string]bool{}
	for _, ref := range signal.EvidenceRefs {
		if err := ref.Validate(); err != nil {
			return signal, err
		}
		encoded, err := jsoncompat.MarshalPython(ref, false)
		if err != nil {
			return signal, err
		}
		if !seen[string(encoded)] {
			refs = append(refs, ref)
			seen[string(encoded)] = true
		}
	}
	signal.EvidenceRefs = refs
	return signal, nil
}

func consolidationHash(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func signalFingerprintPayload(signal ConsolidationSignal) (map[string]any, error) {
	encoded, err := jsoncompat.MarshalPython(signal, false)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	floatText := strconv.FormatFloat(signal.ExpectedValue, 'g', -1, 64)
	if !strings.ContainsAny(floatText, ".eE") {
		floatText += ".0"
	}
	payload["expected_value"] = json.Number(floatText)
	return payload, nil
}

// BuildConsolidationSchedule matches the Python deterministic input and
// priority rules, including deduplication and proposal-only decisions.
func BuildConsolidationSchedule(signals []ConsolidationSignal, window string) (ConsolidationSchedule, error) {
	result := ConsolidationSchedule{Window: strings.TrimSpace(window),
		Entries: []ConsolidationScheduleEntry{}, GroupingKeys: []string{}, ClusterKeys: []string{}}
	if result.Window == "" {
		return result, errors.New("consolidation schedule window is required")
	}
	unique := map[string]ConsolidationSignal{}
	for _, signal := range signals {
		normalized, err := normalizeConsolidationSignal(signal)
		if err != nil {
			return result, err
		}
		payload, err := signalFingerprintPayload(normalized)
		if err != nil {
			return result, err
		}
		encoded, err := jsoncompat.MarshalPython(payload, false)
		if err != nil {
			return result, err
		}
		unique[string(encoded)] = normalized
	}
	canonical := make([]string, 0, len(unique))
	for encoded := range unique {
		canonical = append(canonical, encoded)
	}
	sort.Strings(canonical)
	inputs := make([]any, 0, len(canonical))
	grouping, clusters := map[string]bool{}, map[string]bool{}
	for _, encoded := range canonical {
		var payload any
		decoder := json.NewDecoder(strings.NewReader(encoded))
		decoder.UseNumber()
		if err := decoder.Decode(&payload); err != nil {
			return result, err
		}
		inputs = append(inputs, payload)
		grouping[unique[encoded].GroupingKey] = true
		clusters[unique[encoded].ClusterKey] = true
	}
	inputJSON, err := jsoncompat.MarshalPython(inputs, false)
	if err != nil {
		return result, err
	}
	result.InputSetFingerprintSHA256 = consolidationHash(inputJSON)
	for _, encoded := range canonical {
		signal := unique[encoded]
		fingerprint := consolidationHash([]byte(encoded))
		decision := ConsolidationDecision{DecisionID: "consolidation-" + fingerprint[7:31],
			WorkspaceID: signal.WorkspaceID, SourceEpisodeIDs: signal.SourceEpisodeIDs,
			GroupingKey: signal.GroupingKey, TaskFamily: signal.TaskFamily,
			Trigger: signal.Trigger, ExpectedApplicability: signal.ExpectedApplicability,
			Risk: signal.Risk, CostBudgetTokens: signal.CostBudgetTokens,
			Rationale: signal.Rationale, EvidenceRefs: signal.EvidenceRefs,
			Action: signal.ProposedAction, Status: "proposed"}
		disposition, reason := "scheduled", "priority_eligible"
		if !signal.DependenciesClosed {
			disposition, reason = "deferred", "dependency_closure_incomplete"
		} else if signal.Trigger == "recurrence" && signal.RecurrenceCount < 2 {
			if signal.RareCritical {
				reason = "rare_critical_bypass"
			} else {
				disposition, reason = "deferred", "recurrence_threshold_not_met"
			}
		}
		risk := consolidationRiskPriority[signal.Risk]
		ageCost := float64(signal.AgeSeconds) / float64(signal.EstimatedCostTokens)
		priority := float64(risk)*1000 + signal.ExpectedValue*100 + ageCost
		if signal.RareCritical {
			priority += 10000
		}
		priority = math.RoundToEven(priority*1e9) / 1e9
		result.Entries = append(result.Entries, ConsolidationScheduleEntry{
			Signal: signal, Decision: decision, PriorityScore: priority,
			Disposition: disposition, Reason: reason, signalFingerprint: fingerprint})
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		a, b := result.Entries[i], result.Entries[j]
		if a.Disposition != b.Disposition {
			return a.Disposition == "scheduled"
		}
		if a.Signal.RareCritical != b.Signal.RareCritical {
			return a.Signal.RareCritical
		}
		if consolidationRiskPriority[a.Signal.Risk] != consolidationRiskPriority[b.Signal.Risk] {
			return consolidationRiskPriority[a.Signal.Risk] > consolidationRiskPriority[b.Signal.Risk]
		}
		if a.Signal.ExpectedValue != b.Signal.ExpectedValue {
			return a.Signal.ExpectedValue > b.Signal.ExpectedValue
		}
		ageA := float64(a.Signal.AgeSeconds) / float64(a.Signal.EstimatedCostTokens)
		ageB := float64(b.Signal.AgeSeconds) / float64(b.Signal.EstimatedCostTokens)
		if ageA != ageB {
			return ageA > ageB
		}
		if a.Signal.EstimatedCostTokens != b.Signal.EstimatedCostTokens {
			return a.Signal.EstimatedCostTokens < b.Signal.EstimatedCostTokens
		}
		if a.Signal.Trigger != b.Signal.Trigger {
			return a.Signal.Trigger < b.Signal.Trigger
		}
		if comparison := slices.Compare(a.Signal.SourceEpisodeIDs, b.Signal.SourceEpisodeIDs); comparison != 0 {
			return comparison < 0
		}
		return a.signalFingerprint < b.signalFingerprint
	})
	for i := range result.Entries {
		result.Entries[i].Order = i + 1
	}
	for key := range grouping {
		result.GroupingKeys = append(result.GroupingKeys, key)
	}
	for key := range clusters {
		result.ClusterKeys = append(result.ClusterKeys, key)
	}
	sort.Strings(result.GroupingKeys)
	sort.Strings(result.ClusterKeys)
	basis, err := jsoncompat.MarshalPython(map[string]any{
		"window": result.Window, "input_set_fingerprint_sha256": result.InputSetFingerprintSHA256}, false)
	if err != nil {
		return result, fmt.Errorf("encode consolidation schedule: %w", err)
	}
	result.ScheduleID = "schedule-" + consolidationHash(basis)[7:31]
	return result, nil
}

// ScheduleConsolidation returns only eligible proposal decisions for the
// immediate window. Deferred signals remain visible in BuildConsolidationSchedule.
func ScheduleConsolidation(signals []ConsolidationSignal) ([]ConsolidationDecision, error) {
	schedule, err := BuildConsolidationSchedule(signals, "immediate")
	if err != nil {
		return nil, err
	}
	decisions := make([]ConsolidationDecision, 0, len(schedule.Entries))
	for _, entry := range schedule.Entries {
		if entry.Disposition == "scheduled" {
			decisions = append(decisions, entry.Decision)
		}
	}
	return decisions, nil
}
