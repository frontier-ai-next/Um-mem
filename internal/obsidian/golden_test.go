package obsidian

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestPythonObsidianGoldenOutput(t *testing.T) {
	base := exportFixture()
	base.BlockID, base.Title, base.Content = "mem-a", "Scoped Recall", "Use scoped recall."
	base.Kind, base.Status = "lesson", "accepted"
	base.Authority = "advisory"
	base.TrustLevel, base.VerificationStatus = "workspace_verified", "verified"
	base.VerificationMethods, base.PromotionDecision = []string{"test_run"}, "accepted"
	base.SourceBackend, base.Tags = "mempalace", []string{"testing"}
	base.EvidenceRefs = []memory.EvidenceRef{{RefType: "test_run", RefID: "test-a", ProducedBy: "verifier", ProducerID: "unit"}}
	second := base
	second.BlockID, second.Title, second.Content = "mem-b", "Isolation Rule", "Do not load unrelated memory."
	second.EvidenceRefs = []memory.EvidenceRef{{RefType: "test_run", RefID: "test-b", ProducedBy: "verifier", ProducerID: "unit"}}
	edge := memory.EdgeRecord{SrcID: "mem-a", DstID: "mem-b", EdgeType: "supports", WorkspaceID: "alpha", Reason: "Exact reason"}
	files, coreCount, err := Render([]memory.MemoryRecord{base, second}, []memory.EdgeRecord{edge},
		"alpha", "Alpha Project", []string{"workspace:alpha"})
	if err != nil || coreCount != 2 {
		t.Fatalf("golden setup failed: %d, %v", coreCount, err)
	}
	comparePythonGolden(t, "python_golden.json", files)
}

func TestPythonObsidianEvidenceGoldenOutput(t *testing.T) {
	base := memory.MemoryRecord{BlockID: "session_summary_20260627_210403_ab12",
		Title: "Session Summary", Content: "A raw session account.", Kind: "session_summary",
		Status: "candidate", Retention: "durable", DeliveryPolicy: "on_demand",
		Temperature: "warm", Authority: "advisory", TrustLevel: "agent_reported",
		Scope: "workspace", VerificationStatus: "unverified", PromotionDecision: "not_requested",
		WorkspaceID: "alpha", RunID: "20260627_210403_ab12", Sensitivity: "internal",
		ContentMode: "full", OperationalRisk: "low"}
	turn := base
	turn.BlockID, turn.Title, turn.Content, turn.Kind = "turn_20260627_210403_ab12_1",
		"Conversation turn", "User asked a question.", "session_turn"
	files, count, err := Render([]memory.MemoryRecord{base, turn}, nil, "alpha", "Alpha Project",
		[]string{"workspace:alpha"})
	if err != nil || count != 0 {
		t.Fatalf("evidence golden setup failed: %d, %v", count, err)
	}
	comparePythonGolden(t, "python_evidence_golden.json", files)
}

func comparePythonGolden(t *testing.T, name string, files map[string]string) {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{}
	if err := json.Unmarshal(fixture, &expected); err != nil {
		t.Fatal(err)
	}
	for path, want := range expected {
		got, exists := files[path]
		if !exists {
			t.Errorf("Go omitted Python note %s", path)
			continue
		}
		if got == want {
			continue
		}
		gotLines, wantLines := strings.Split(got, "\n"), strings.Split(want, "\n")
		for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
			var left, right string
			if i < len(gotLines) {
				left = gotLines[i]
			}
			if i < len(wantLines) {
				right = wantLines[i]
			}
			if left != right {
				t.Errorf("%s line %d: Go %q, Python %q", path, i+1, left, right)
				break
			}
		}
	}
	for path := range files {
		if _, exists := expected[path]; !exists {
			t.Errorf("Go added unexpected note %s", path)
		}
	}
}
