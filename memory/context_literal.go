package memory

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
)

func normalizedLiteral(value string) string {
	return cases.Fold().String(strings.ReplaceAll(value, "\\", "/"))
}

// MatchesEvidenceLiteral retains the Python contract's exact alphanumeric
// boundaries. Query words alone never alter backend order or excerpts.
func MatchesEvidenceLiteral(text, literal string) bool {
	haystack := []rune(normalizedLiteral(text))
	needle := []rune(normalizedLiteral(strings.TrimSpace(literal)))
	if len(haystack) == 0 || len(needle) == 0 {
		return false
	}
	for start := 0; start+len(needle) <= len(haystack); start++ {
		if string(haystack[start:start+len(needle)]) != string(needle) {
			continue
		}
		end := start + len(needle)
		beforeOK := start == 0 || !unicode.IsLetter(haystack[start-1]) && !unicode.IsDigit(haystack[start-1])
		afterOK := end == len(haystack) || !unicode.IsLetter(haystack[end]) && !unicode.IsDigit(haystack[end])
		if beforeOK && afterOK {
			return true
		}
	}
	return false
}

func TruncateToTokens(text string, maxTokens int) string {
	if maxTokens <= 0 {
		return ""
	}
	if EstimateTokens(text) <= maxTokens {
		return text
	}
	characters := []rune(text)
	lower, upper := 0, len(characters)
	for lower < upper {
		middle := (lower + upper + 1) / 2
		if EstimateTokens(string(characters[:middle])) <= maxTokens {
			lower = middle
		} else {
			upper = middle - 1
		}
	}
	return strings.TrimRightFunc(string(characters[:lower]), unicode.IsSpace)
}

type literalRange struct{ start, end int }

func windowAroundLiteral(text []rune, span literalRange, maxTokens int) string {
	if maxTokens <= 0 || EstimateTokens(string(text[span.start:span.end])) > maxTokens {
		return ""
	}
	targetChars := max(span.end-span.start, maxTokens*4-4)
	surrounding := max(0, targetChars-(span.end-span.start))
	before := min(span.start, surrounding/2)
	after := min(len(text)-span.end, surrounding-before)
	before = min(span.start, surrounding-after)
	begin, end := span.start-before, span.end+after
	render := func() string {
		excerpt := strings.TrimSpace(string(text[begin:end]))
		if begin > 0 {
			excerpt = "..." + excerpt
		}
		if end < len(text) {
			excerpt += "..."
		}
		return excerpt
	}
	excerpt := render()
	for excerpt != "" && EstimateTokens(excerpt) > maxTokens {
		if begin < span.start {
			begin++
		} else if end > span.end {
			end--
		} else {
			return ""
		}
		excerpt = render()
	}
	return excerpt
}

func exactLiteralExcerpt(text string, maxTokens int, literals []string) string {
	if maxTokens <= 0 {
		return ""
	}
	characters := []rune(text)
	folded := []rune(cases.Fold().String(text))
	ranges := []literalRange{}
	for _, literal := range literals {
		needle := []rune(cases.Fold().String(literal))
		for start := 0; start+len(needle) <= len(folded); start++ {
			if string(folded[start:start+len(needle)]) == string(needle) {
				ranges = append(ranges, literalRange{start, start + len(needle)})
				break
			}
		}
	}
	if len(ranges) == 0 {
		return ""
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].start != ranges[j].start {
			return ranges[i].start < ranges[j].start
		}
		return ranges[i].end < ranges[j].end
	})
	merged := []literalRange{}
	for _, span := range ranges {
		if len(merged) > 0 && span.start <= merged[len(merged)-1].end+64 {
			merged[len(merged)-1].end = max(merged[len(merged)-1].end, span.end)
		} else {
			merged = append(merged, span)
		}
	}
	separator := "\n...\n"
	allowances := make([]int, len(merged))
	required := max(0, len(merged)-1) * EstimateTokens(separator)
	for i, span := range merged {
		if span.end > len(characters) {
			return ""
		}
		allowances[i] = EstimateTokens(string(characters[span.start:span.end]))
		required += allowances[i]
	}
	if required > maxTokens {
		return ""
	}
	remaining := maxTokens - required
	for i := range allowances {
		extra := remaining / (len(allowances) - i)
		allowances[i] += extra
		remaining -= extra
	}
	excerpts := []string{}
	for i, span := range merged {
		if excerpt := windowAroundLiteral(characters, span, allowances[i]); excerpt != "" {
			excerpts = append(excerpts, excerpt)
		}
	}
	rendered := strings.Join(excerpts, separator)
	if EstimateTokens(rendered) > maxTokens {
		return ""
	}
	return rendered
}

func ExcerptToTokens(text string, maxTokens int, literals []string) string {
	if maxTokens <= 0 || EstimateTokens(text) <= maxTokens {
		return TruncateToTokens(text, maxTokens)
	}
	supported := []string{}
	for _, literal := range literals {
		if MatchesEvidenceLiteral(text, literal) {
			supported = append(supported, literal)
		}
	}
	if len(supported) > 0 {
		capsule := exactLiteralExcerpt(text, maxTokens, supported)
		if capsule != "" {
			all := true
			for _, literal := range supported {
				if !MatchesEvidenceLiteral(capsule, literal) {
					all = false
					break
				}
			}
			if all {
				return capsule
			}
		}
	}
	return TruncateToTokens(text, maxTokens)
}
