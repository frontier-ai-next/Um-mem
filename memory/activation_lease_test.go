package memory

import (
	"strings"
	"testing"
)

func TestActivationLeasePreservesFinalPacketWithoutPromotingAuthority(t *testing.T) {
	plan := NewRecallPlan("parser")
	plan.WorkspaceID = "alpha"
	plan.LogicalBlockIDs = []string{"code_resources"}
	plan.Reason = "explicit_block_first_recall"
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	packed := PackedEvidencePayload{Items: []map[string]any{{
		"text": "Parser implementation evidence", "source": "code", "authority": "directive",
		"metadata": map[string]any{"block_id": "code", "authority": "directive"},
	}}, RenderedContext: "Parser implementation evidence", TokenBudget: 256,
		EstimatedTokens: EstimateTokens("Parser implementation evidence")}
	lease, err := ActivationLeaseFromPayload(plan, packed, "mempalace", "deep_block_local")
	if err != nil || len(lease.Items) != 1 || lease.Items[0].Authority != "non_directive" ||
		lease.Items[0].Surface != "supplemental_evidence" || lease.Items[0].Metadata["authority"] != "non_directive" ||
		lease.Items[0].Metadata["source_authority"] != "directive" ||
		lease.RenderedContext != packed.RenderedContext || lease.Trace.Mode != "deep_block_local" ||
		!lease.Trace.OutOfPrompt || strings.Contains(lease.RenderedContext, "activation_trace") ||
		!strings.HasPrefix(lease.LeaseID, "lease_") {
		t.Fatalf("activation lease changed packet or authority: %+v %v", lease, err)
	}
}
