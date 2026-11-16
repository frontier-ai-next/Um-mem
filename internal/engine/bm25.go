package engine

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// bm25 is the lexical half of retrieval. Pure word-overlap rerank cannot match a
// question that uses a synonym of the stored title, and the semantic backend alone
// drifts on short queries; BM25 with idf at least ranks "ingest" above "git"
// when the question is about adding a source.
type bm25Index struct {
	docs   []memory.MemoryRecord
	freq   []map[string]int
	length []int
	df     map[string]int
	avgLen float64
}

func bm25Tokenize(text string) []string {
	tokens := make([]string, 0, 32)
	for _, field := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(field)) < 3 || linkStopWords[field] {
			continue
		}
		tokens = append(tokens, field)
	}
	return tokens
}

func buildBM25(records []memory.MemoryRecord) *bm25Index {
	index := &bm25Index{
		docs:   make([]memory.MemoryRecord, 0, len(records)),
		freq:   make([]map[string]int, 0, len(records)),
		length: make([]int, 0, len(records)),
		df:     map[string]int{},
	}
	total := 0
	for _, record := range records {
		tokens := bm25Tokenize(record.Title + " " + record.Content)
		if len(tokens) == 0 {
			continue
		}
		counts := make(map[string]int, len(tokens))
		for _, token := range tokens {
			counts[token]++
		}
		for token := range counts {
			index.df[token]++
		}
		index.docs = append(index.docs, record)
		index.freq = append(index.freq, counts)
		index.length = append(index.length, len(tokens))
		total += len(tokens)
	}
	if len(index.docs) > 0 {
		index.avgLen = float64(total) / float64(len(index.docs))
	}
	return index
}

func (index *bm25Index) score(queryTokens []string, doc int) float64 {
	const k1, b = 1.4, 0.75
	n := float64(len(index.docs))
	if n == 0 {
		return 0
	}
	score := 0.0
	length := float64(index.length[doc])
	for _, token := range queryTokens {
		tf := float64(index.freq[doc][token])
		if tf == 0 {
			continue
		}
		df := float64(index.df[token])
		idf := math.Log(1 + (n-df+0.5)/(df+0.5))
		denom := tf + k1*(1-b+b*length/index.avgLen)
		score += idf * tf * (k1 + 1) / denom
	}
	return score
}

// search returns the best lexical matches with normalised scores.
func (index *bm25Index) search(query string, limit int) map[string]float64 {
	tokens := bm25Tokenize(query)
	if len(tokens) == 0 || len(index.docs) == 0 || limit <= 0 {
		return nil
	}
	scored := make([]struct {
		id    string
		score float64
	}, 0, len(index.docs))
	best := 0.0
	for i, record := range index.docs {
		value := index.score(tokens, i)
		if value <= 0 {
			continue
		}
		if value > best {
			best = value
		}
		scored = append(scored, struct {
			id    string
			score float64
		}{id: record.BlockID, score: value})
	}
	if best == 0 {
		return nil
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	if len(scored) > limit {
		scored = scored[:limit]
	}
	out := make(map[string]float64, len(scored))
	for _, item := range scored {
		out[item.id] = item.score / best
	}
	return out
}
