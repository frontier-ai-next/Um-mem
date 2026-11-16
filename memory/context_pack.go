package memory

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

type evidenceCard struct {
	original          string
	item              map[string]any
	exactLiterals     []string
	minimumFact       string
	upstreamTruncated bool
}

func evidenceIdentity(item map[string]any) (string, string) {
	metadata := contextMap(item["metadata"])
	membership := contextMap(metadata["block_membership"])
	return contextFirst(metadata["block_id"], item["source"], item["id"]),
		contextFirst(membership["primary_block_id"])
}

func evidenceHeader(item map[string]any, index int, truncated, compact bool) string {
	metadata := contextMap(item["metadata"])
	membership := contextMap(metadata["block_membership"])
	blockID := contextFirst(membership["primary_block_id"], "unknown")
	recordID := contextFirst(metadata["block_id"], item["source"], "unknown")
	title := contextFirst(item["title"], metadata["title"], "memory")
	status := contextFirst(metadata["status"], item["status"], "unknown")
	lifecycle := "current"
	if slices.Contains([]string{"superseded", "deprecated", "retracted", "expired"}, status) {
		lifecycle = "superseded"
	}
	temporal := []string{}
	for _, key := range []string{"source_dates", "source_speakers"} {
		if contextValue(metadata[key]) {
			temporal = append(temporal, key+"="+contextText(metadata[key]))
		}
	}
	if metadata["historical"] == true {
		lifecycle = "historical"
		temporal = append(temporal, "temporal=historical")
	}
	for _, key := range []string{"as_of", "known_as_of", "event_time_status", "valid_from", "valid_to", "expires_at"} {
		if compact && lifecycle == "current" && key == "valid_from" {
			continue
		}
		if value, ok := metadata[key].(string); ok && value != "" {
			temporal = append(temporal, key+"="+contextRunes(value, 80))
		}
	}
	temporalSuffix := ""
	if len(temporal) > 0 {
		temporalSuffix = " " + strings.Join(temporal, " ")
	}
	subjectSuffix := ""
	for _, role := range contextSlice(metadata["evidence_roles"]) {
		if role == "negative_evidence" {
			subject, attribution := "unattributed", "missing"
			for _, key := range []string{"subject_id", "belief_holder_id", "speaker_id", "actor_id"} {
				if value := contextNormalized(contextText(metadata[key]), 160); value != "" {
					subject, attribution = value, key
					break
				}
			}
			subjectSuffix = " subject=" + subject + " attribution=" + attribution
			break
		}
	}
	truncatedText := "false"
	if truncated {
		truncatedText = "true"
	}
	trust := contextFirst(metadata["trust_level"], "agent_reported")
	verification := contextFirst(metadata["verification_status"], "unverified")
	if compact {
		statusCodes := map[string]string{"accepted": "a", "candidate": "c", "superseded": "s",
			"deprecated": "d", "retracted": "r", "expired": "e"}
		trustCodes := map[string]string{"agent_reported": "ar", "workspace_verified": "wv"}
		verificationCodes := map[string]string{"unverified": "u", "observed": "o",
			"evidence_backed": "eb", "verified": "v", "disputed": "d"}
		if replacement := statusCodes[status]; replacement != "" {
			status = replacement
		}
		if replacement := trustCodes[trust]; replacement != "" {
			trust = replacement
		}
		if replacement := verificationCodes[verification]; replacement != "" {
			verification = replacement
		}
		return fmt.Sprintf("E%d: record=%s block=%s title=%s status=%s/%s trust=%s/%s truncated=%s%s%s",
			index, recordID, blockID, title, lifecycle, status, trust, verification,
			truncatedText, subjectSuffix, temporalSuffix)
	}
	path := contextFirst(metadata["logical_block_path"])
	if path == "" && blockID != "unknown" {
		path = blockID
	}
	if path == "" {
		path = contextFirst(item["source_path"], metadata["source_path"], "-")
	}
	tags := []string{}
	for _, tag := range contextSlice(item["tags"]) {
		tags = append(tags, contextText(tag))
		if len(tags) == 8 {
			break
		}
	}
	tagText := strings.Join(tags, ",")
	if tagText == "" {
		tagText = "-"
	}
	return fmt.Sprintf("Evidence card %d: block=%s path=%s record=%s title=%s status=%s/%s revision=%s source=%s@%s trust=%s/%s tags=%s truncated=%s%s%s",
		index, blockID, path, recordID, title, lifecycle, status,
		contextFirst(metadata["revision_id"], "unknown"),
		contextFirst(metadata["producer"], metadata["session_id"], metadata["source_backend"], item["source"], "memory"),
		contextFirst(metadata["recorded_at"], "unknown"), trust, verification, tagText,
		truncatedText, subjectSuffix, temporalSuffix)
}

func minimumExactExcerpt(text string, literals []string) string {
	if len(literals) == 0 {
		return ExcerptToTokens(text, 6, nil)
	}
	minimum := 6
	for _, literal := range literals {
		minimum += EstimateTokens(literal)
	}
	minimum += 4
	maximum := max(minimum, EstimateTokens(text))
	budget := minimum
	for budget <= maximum {
		excerpt := ExcerptToTokens(text, budget, literals)
		all := true
		for _, literal := range literals {
			if !MatchesEvidenceLiteral(excerpt, literal) {
				all = false
				break
			}
		}
		if all {
			return excerpt
		}
		if budget == maximum {
			break
		}
		budget = min(maximum, max(budget+8, budget*2))
	}
	return text
}

func evidenceMinimumChars(cards []evidenceCard, compact bool) int {
	lines := []string{evidenceContextPrefix}
	for i, card := range cards {
		lines = append(lines, evidenceHeader(card.item, i+1, false, compact), "fact="+card.minimumFact)
	}
	return contextLength(strings.Join(lines, "\n"))
}

func evidenceMetadataTokens(items []map[string]any, compact bool) int {
	lines := []string{evidenceContextPrefix}
	for i, item := range items {
		lines = append(lines, evidenceHeader(item, i+1, item["truncated"] == true, compact), "fact=")
	}
	return EstimateTokens(strings.Join(lines, "\n"))
}

func selectEvidencePrefix(cards []evidenceCard, budget int, compact bool) []evidenceCard {
	selected := []evidenceCard{}
	for _, card := range cards {
		candidate := append(slices.Clone(selected), card)
		if evidenceMinimumChars(candidate, compact) > budget {
			break
		}
		selected = candidate
	}
	return selected
}

func factWithinChars(card evidenceCard, allowance int) string {
	if allowance >= contextLength(card.original) {
		return card.original
	}
	if len(card.exactLiterals) == 0 {
		return strings.TrimSpace(contextRunes(card.original, allowance))
	}
	candidate := ExcerptToTokens(card.original, max(1, allowance/4), card.exactLiterals)
	if contextLength(candidate) <= allowance {
		all := true
		for _, literal := range card.exactLiterals {
			if !MatchesEvidenceLiteral(candidate, literal) {
				all = false
				break
			}
		}
		if all {
			return candidate
		}
	}
	if contextLength(card.minimumFact) <= allowance {
		return card.minimumFact
	}
	return strings.TrimSpace(contextRunes(card.minimumFact, allowance))
}

// PackEvidencePayload is the final, ordered evidence budget boundary. It does
// not rerank candidates and its output is supplemental, non-directive context.
func PackEvidencePayload(rawItems []map[string]any, options PackOptions) (PackedEvidencePayload, error) {
	if options.MaxTokens > math.MaxInt/4 {
		return PackedEvidencePayload{}, errors.New("context token budget is too large")
	}
	budget := max(0, options.MaxTokens)
	charBudget := budget * 4
	result := PackedEvidencePayload{Items: []map[string]any{}, TokenBudget: budget,
		ExactLiteralCapsule: []string{}, Visibility: []EvidenceVisibility{}, ExactLiteralLosses: []ExactLiteralLoss{}}
	if charBudget <= 0 {
		return result, nil
	}
	if charBudget <= contextLength(evidenceContextPrefix) {
		result.RenderedContext = contextRunes(evidenceContextPrefix, charBudget)
		result.EstimatedTokens = EstimateTokens(result.RenderedContext)
		result.MetadataTokens = result.EstimatedTokens
		return result, nil
	}
	literals := []string{}
	for _, requirement := range options.Requirements {
		if strings.TrimSpace(requirement.Subject) == "" || !slices.Contains([]string{
			"policy", "source", "decision", "failure", "test", "temporal_state", "rationale", "negative_evidence",
		}, requirement.Role) {
			return result, errors.New("evidence requirement identity is invalid")
		}
		for _, literal := range requirement.AcceptedLiterals {
			literal = strings.TrimSpace(literal)
			if literal != "" && !slices.Contains(literals, literal) {
				literals = append(literals, literal)
			}
		}
	}
	cards := []evidenceCard{}
	for _, raw := range rawItems {
		if !contextRenderable(raw) {
			continue
		}
		original := strings.TrimSpace(contextFirst(raw["text"], raw["content"]))
		if original == "" {
			continue
		}
		item := compactContextItem(raw, options.SourceFallback)
		item["text"] = original
		supported := []string{}
		for _, literal := range literals {
			if MatchesEvidenceLiteral(original, literal) {
				supported = append(supported, literal)
			}
		}
		minimumFact := original
		if len(supported) > 0 {
			minimumFact = minimumExactExcerpt(original, supported)
		}
		cards = append(cards, evidenceCard{original: original, item: item,
			exactLiterals: supported, minimumFact: minimumFact,
			upstreamTruncated: item["truncated"] == true || contextMap(item["metadata"])["truncated"] == true})
	}
	if len(cards) == 0 {
		return result, nil
	}
	selected := selectEvidencePrefix(cards, charBudget, false)
	compact := false
	if len(selected) < len(cards) {
		compactSelected := selectEvidencePrefix(cards, charBudget, true)
		if len(compactSelected) >= len(selected) {
			selected, compact = compactSelected, true
		}
	}
	if len(selected) == 0 {
		first := cards[0]
		first.minimumFact = minimumExactExcerpt(first.original, first.exactLiterals)
		selected = selectEvidencePrefix([]evidenceCard{first}, charBudget, true)
		compact = true
	}
	if len(selected) == 0 {
		result.RenderedContext = contextRunes(evidenceContextPrefix, charBudget)
		result.EstimatedTokens = EstimateTokens(result.RenderedContext)
		result.MetadataTokens = result.EstimatedTokens
		result.TruncatedItemCount = len(cards)
		for _, card := range cards {
			id, block := evidenceIdentity(card.item)
			result.Visibility = append(result.Visibility, EvidenceVisibility{id, block, "not_visible", "context_budget_item_omitted"})
		}
		return result, nil
	}
	baseLines := []string{evidenceContextPrefix}
	for i, card := range selected {
		baseLines = append(baseLines, evidenceHeader(card.item, i+1, false, compact), "fact=")
	}
	remaining := max(0, charBudget-contextLength(strings.Join(baseLines, "\n")))
	allowances := make([]int, len(selected))
	for i, card := range selected {
		allowances[i] = contextLength(card.minimumFact)
		remaining -= allowances[i]
	}
	distributable := max(0, remaining)
	for i, card := range selected {
		wanted := max(0, contextLength(card.original)-allowances[i])
		granted := min(distributable, wanted)
		allowances[i] += granted
		distributable -= granted
		if distributable <= 0 {
			break
		}
	}
	for i, card := range selected {
		fact := factWithinChars(card, allowances[i])
		item := make(map[string]any, len(card.item)+1)
		for key, value := range card.item {
			item[key] = value
		}
		item["text"] = fact
		contextTruncated := fact != card.original
		truncated := card.upstreamTruncated || contextTruncated
		item["truncated"] = truncated
		metadata := make(map[string]any, len(contextMap(item["metadata"]))+3)
		for key, value := range contextMap(item["metadata"]) {
			metadata[key] = value
		}
		renderedLiterals := []string{}
		for _, literal := range card.exactLiterals {
			if MatchesEvidenceLiteral(fact, literal) {
				renderedLiterals = append(renderedLiterals, literal)
			} else {
				id, _ := evidenceIdentity(item)
				result.ExactLiteralLosses = append(result.ExactLiteralLosses,
					ExactLiteralLoss{literal, id, "exact_literal_exceeds_context_budget"})
			}
		}
		if len(renderedLiterals) > 0 {
			metadata["exact_literal_capsule"] = renderedLiterals
			for _, literal := range renderedLiterals {
				if !slices.Contains(result.ExactLiteralCapsule, literal) {
					result.ExactLiteralCapsule = append(result.ExactLiteralCapsule, literal)
				}
			}
		} else {
			delete(metadata, "exact_literal_capsule")
		}
		state, reason := "full_visible", ""
		if truncated {
			state = "partial_visible"
			switch {
			case card.upstreamTruncated && contextTruncated:
				reason = "upstream_and_context_budget"
			case card.upstreamTruncated:
				reason = "upstream_truncation"
			default:
				reason = "context_budget"
			}
			metadata["truncation_reason"] = reason
		} else {
			delete(metadata, "truncation_reason")
		}
		metadata["visibility"] = state
		item["metadata"] = metadata
		result.Items = append(result.Items, item)
		id, block := evidenceIdentity(item)
		result.Visibility = append(result.Visibility, EvidenceVisibility{id, block, state, reason})
	}
	for _, card := range cards[len(selected):] {
		id, block := evidenceIdentity(card.item)
		result.Visibility = append(result.Visibility, EvidenceVisibility{id, block, "not_visible", "context_budget_item_omitted"})
		for _, literal := range card.exactLiterals {
			result.ExactLiteralLosses = append(result.ExactLiteralLosses,
				ExactLiteralLoss{literal, id, "candidate_omitted_by_context_budget"})
		}
	}
	lines := []string{evidenceContextPrefix}
	for i, item := range result.Items {
		lines = append(lines, evidenceHeader(item, i+1, item["truncated"] == true, compact),
			"fact="+contextText(item["text"]))
		if item["truncated"] == true {
			result.TruncatedItemCount++
		}
	}
	result.RenderedContext = strings.Join(lines, "\n")
	result.EstimatedTokens = EstimateTokens(result.RenderedContext)
	result.MetadataTokens = evidenceMetadataTokens(result.Items, compact)
	return result, nil
}
