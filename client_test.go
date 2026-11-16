package ummem_test

import (
	"context"
	"os"
	"strings"
	"testing"

	ummem "github.com/frontier-ai-next/Um-mem"
	"github.com/frontier-ai-next/Um-mem/internal/telemetry"
)

func TestExternalGoClientCanWriteAndRecall(t *testing.T) {
	repo := t.TempDir()
	client, err := ummem.Open(ummem.Config{RepoRoot: repo, WorkspaceID: "alpha",
		ProfileID: "default", BackendID: "mnemosyne"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	result, err := client.RememberCandidate(ctx, ummem.CandidateRequest{
		Content: "The project release rule is R7Q.", Title: "Project release rule",
		Kind: "project_fact", EvidenceKind: "user_statement",
		RequestedBlockID: "stable_project_facts",
	})
	if err != nil || !result.Saved || result.RecordID == "" {
		t.Fatalf("embedded Go write failed: %+v, %v", result, err)
	}
	plan := ummem.NewRecallPlan("project release rule")
	plan.WorkspaceID, plan.ProfileID = "alpha", "default"
	plan.ScopeChain = []string{"workspace:alpha"}
	plan.LogicalBlockIDs = []string{"stable_project_facts"}
	plan.IncludeCandidates = true
	hits, err := client.Recall(ctx, plan)
	if err != nil || len(hits) != 1 || hits[0].BlockID != result.RecordID {
		t.Fatalf("embedded Go recall failed: write=%+v hits=%+v plan=%+v error=%v", result, hits, plan, err)
	}
	dependencies, err := client.ValidateDependencies(ctx, result.RecordID, ummem.DependencyOptions{})
	if err != nil || dependencies.Status != "valid" || dependencies.RecordID != result.RecordID {
		t.Fatalf("embedded dependency receipt failed: %+v, %v", dependencies, err)
	}
	exact, err := client.ReadExactRecord(ctx, result.RecordID, "")
	if err != nil || exact == nil || exact.Content != "The project release rule is R7Q." {
		t.Fatalf("embedded exact read failed: %+v, %v", exact, err)
	}
	if _, err := client.InspectRecord(ctx, result.RecordID,
		"stable_project_facts", "wrong-revision"); err == nil {
		t.Fatal("embedded inspection accepted a stale revision")
	}
	if inspected, err := client.InspectRecord(ctx, result.RecordID,
		"stable_project_facts", exact.RevisionID); err != nil || inspected.BlockID != result.RecordID {
		t.Fatalf("embedded exact inspection failed: %+v, %v", inspected, err)
	}
	packed, err := client.BuildContext(ctx, ummem.RecallArgs{
		Query: "project release rule", RecordIDs: []string{result.RecordID},
	}, []string{"workspace:alpha"}, "", true)
	if err != nil || packed["ok"] != true || len(packed["items"].([]map[string]any)) != 1 {
		t.Fatalf("embedded context build failed: %+v, %v", packed, err)
	}
	lease, err := client.BuildActivationLease(ctx, ummem.RecallArgs{
		Query: "project release rule", RecordIDs: []string{result.RecordID},
	}, []string{"workspace:alpha"}, "run-1")
	if err != nil || lease.RunID != "run-1" || len(lease.Items) != 1 ||
		lease.Items[0].BlockID != result.RecordID ||
		lease.Items[0].Authority != "non_directive" || !lease.Trace.OutOfPrompt {
		t.Fatalf("embedded activation lease lost its advisory boundary: %+v, %v", lease, err)
	}
	profile, err := telemetry.ProfileReport(repo, 100)
	if err != nil || profile["max_in_flight"] != 2 {
		t.Fatalf("context build did not record nested Go spans: %+v %v", profile, err)
	}
	operations := profile["by_operation"].(map[string]any)
	if operations["build_context"] == nil || operations["backend_recall"] == nil {
		t.Fatalf("context profile omitted an operation: %+v", operations)
	}
	trace, err := os.ReadFile(telemetry.Path(repo))
	if err != nil || strings.Contains(string(trace), "project release rule") {
		t.Fatalf("profile trace exposed the recall query: %v", err)
	}
	health, err := client.BlockHealth(ctx, []string{"stable_project_facts"})
	if err != nil {
		t.Fatal(err)
	}
	metrics := health["block_metrics"].([]map[string]any)
	if len(metrics) != 1 || metrics[0]["rendered_tokens"].(int) <= 0 {
		t.Fatalf("rendered context tokens were not attributed to the native leaf: %+v", metrics)
	}
}
