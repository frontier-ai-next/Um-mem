package memory

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type TextRank struct {
	Index int
	Score float64
	Terms []string
}

func textTokens(value string) []string {
	value = cases.Fold().String(norm.NFKC.String(value))
	result := make([]string, 0)
	var token strings.Builder
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsNumber(char) {
			token.WriteRune(char)
		} else if token.Len() > 0 {
			result = append(result, token.String())
			token.Reset()
		}
	}
	if token.Len() > 0 {
		result = append(result, token.String())
	}
	return result
}

func textGrams(value string) map[string]bool {
	runes := []rune(value)
	result := map[string]bool{}
	if len(runes) == 0 {
		return result
	}
	if len(runes) <= 3 {
		result[string(runes)] = true
		return result
	}
	for index := 0; index+3 <= len(runes); index++ {
		result[string(runes[index:index+3])] = true
	}
	return result
}

func textStream(tokens []string) string { return strings.Join(tokens, "") }

func textOccurs(term string, tokens []string, stream string) bool {
	for _, token := range tokens {
		if token == term {
			return true
		}
	}
	if len([]rune(term)) < 4 {
		return false
	}
	needle := textGrams(term)
	if len(needle) == 0 {
		return false
	}
	haystack := textGrams(stream)
	matched := 0
	for gram := range needle {
		if haystack[gram] {
			matched++
		}
	}
	return float64(matched)/float64(len(needle)) >= 0.55
}

// RankText matches the Python local tier's corpus-weighted lexical fallback.
// It accepts titles separately because title matches are a routing signal.
func RankText(query string, documents, titles []string, limit int) []TextRank {
	terms := make([]string, 0)
	seen := map[string]bool{}
	for _, term := range textTokens(query) {
		if !seen[term] {
			terms = append(terms, term)
			seen[term] = true
		}
	}
	if len(terms) == 0 || len(documents) == 0 {
		return nil
	}
	titleAvailable := len(titles) == len(documents)
	documentTokens := make([][]string, len(documents))
	documentStreams := make([]string, len(documents))
	titleTokens := make([][]string, len(documents))
	titleStreams := make([]string, len(documents))
	for index, document := range documents {
		documentTokens[index] = textTokens(document)
		documentStreams[index] = textStream(documentTokens[index])
		if titleAvailable {
			titleTokens[index] = textTokens(titles[index])
			titleStreams[index] = textStream(titleTokens[index])
		}
	}
	total := len(documents)
	df := make(map[string]int, len(terms))
	for _, term := range terms {
		for index := range documents {
			if textOccurs(term, documentTokens[index], documentStreams[index]) {
				df[term]++
			}
		}
	}
	commonLimit := max(1, total/3)
	veryCommonLimit := max(2, total-1)
	type scored struct {
		index int
		raw   float64
		terms []string
	}
	rows := make([]scored, 0)
	for index := range documents {
		row := scored{index: index}
		for _, term := range terms {
			if !textOccurs(term, documentTokens[index], documentStreams[index]) {
				continue
			}
			titleHit := titleAvailable && textOccurs(term, titleTokens[index], titleStreams[index])
			if df[term] >= veryCommonLimit || df[term] > commonLimit && !titleHit {
				continue
			}
			weight := math.Log(float64(total+1)/float64(df[term]+1)) + 1
			if titleHit {
				weight *= 1.75
			}
			row.raw += weight
			row.terms = append(row.terms, term)
		}
		if len(row.terms) > 0 {
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].raw != rows[j].raw {
			return rows[i].raw > rows[j].raw
		}
		return rows[i].index < rows[j].index
	})
	if len(rows) == 0 {
		return nil
	}
	best := rows[0].raw
	result := make([]TextRank, 0, len(rows))
	for _, row := range rows {
		if row.raw < best*0.5 {
			continue
		}
		result = append(result, TextRank{Index: row.index,
			Score: math.Round(row.raw/best*10000) / 10000, Terms: row.terms})
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result
}
