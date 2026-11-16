package obsidian

import (
	"testing"

	"github.com/frontier-ai-next/Um-mem/memory"
)

func exportFixture() memory.MemoryRecord {
	return memory.MemoryRecord{BlockID: "mem_1", Title: "A/B: Test", Content: "Exact content",
		Kind: "fact", Status: "accepted", Retention: "durable", DeliveryPolicy: "on_demand",
		Temperature: "warm", Scope: "workspace", WorkspaceID: "alpha", Sensitivity: "internal",
		ContentMode: "full", OperationalRisk: "low", Tags: []string{}}
}

func TestObsidianClassifiesAndFiltersExactRecords(t *testing.T) {
	base := exportFixture()
	if got := noteName(base); got != "A B Test - mem_1" {
		t.Fatalf("note name differs from safe Obsidian path: %q", got)
	}
	if area(base) != "Project knowledge" || evidenceArchive(base) {
		t.Fatalf("accepted fact was not core memory: %s", area(base))
	}
	candidate := base
	candidate.BlockID, candidate.Status = "candidate-1", "candidate"
	if area(candidate) != "Candidate ideas" || !evidenceArchive(candidate) {
		t.Fatal("candidate leaked into the core memory tree")
	}
	protected := base
	protected.BlockID, protected.Sensitivity = "protected-1", "secret"
	foreign := base
	foreign.BlockID, foreign.WorkspaceID = "foreign-1", "other"
	core, evidence := selectRecords([]memory.MemoryRecord{base, candidate, protected, foreign},
		[]string{"workspace:alpha"})
	if len(core) != 1 || core[0].BlockID != base.BlockID ||
		len(evidence) != 1 || evidence[0].BlockID != candidate.BlockID {
		t.Fatalf("scope or safety filtering changed: core=%+v evidence=%+v", core, evidence)
	}
}
