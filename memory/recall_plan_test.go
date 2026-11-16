package memory

import "testing"

func TestRecallPlanStructuralQueryTypes(t *testing.T) {
	cases := []struct {
		name string
		set  func(*RecallPlan)
		want string
	}{
		{"record", func(p *RecallPlan) { p.RecordIDs = []string{"record-1"} }, "exact_lookup"},
		{"path", func(p *RecallPlan) { p.Paths = []string{"src/parser.py"} }, "exact_lookup"},
		{"words", func(*RecallPlan) {}, "broad_recall"},
		{"temporal", func(p *RecallPlan) { p.TemporalMode = "historical" }, "temporal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := NewRecallPlan("  INSPECT  parser  ")
			tc.set(&plan)
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
			if plan.QueryType != tc.want || plan.NormalizedQuery != "inspect parser" {
				t.Fatalf("type=%q normalized=%q", plan.QueryType, plan.NormalizedQuery)
			}
		})
	}
}

func TestRecallPlanRejectsUnsafeCombinations(t *testing.T) {
	cases := []struct {
		name string
		set  func(*RecallPlan)
	}{
		{"broad-and-leaf", func(p *RecallPlan) { p.SearchAll = true; p.LogicalBlockIDs = []string{"leaf"} }},
		{"hybrid-history", func(p *RecallPlan) { p.RetrievalMode = "hybrid"; p.TemporalMode = "historical" }},
		{"naive-knowledge-time", func(p *RecallPlan) { p.KnownAsOf = "2020-01-15T00:00:00" }},
		{"current-revision", func(p *RecallPlan) { p.RevisionID = "rev-1" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := NewRecallPlan("framework")
			tc.set(&plan)
			if err := plan.Validate(); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
}
