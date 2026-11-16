package mcpserver

import "testing"

func TestObsidianMCPExportScopeUsesHostLineage(t *testing.T) {
	t.Setenv("UM_MEM_SESSION_ID", "session-1")
	t.Setenv("UM_MEM_USER_ID", "user-a")
	if chain, err := boundMCPExportScope("alpha", nil); err != nil || len(chain) != 2 ||
		chain[0] != "session:session-1" || chain[1] != "workspace:alpha" {
		t.Fatalf("default export scope changed: %v, %v", chain, err)
	}
	for _, chain := range [][]string{{"workspace:other"}, {"workspace:alpha", "user_global:user-b"},
		{"workspace:alpha", "session:session-1"}, {"workspace:alpha", "workspace:alpha"}} {
		if _, err := boundMCPExportScope("alpha", chain); err == nil {
			t.Fatalf("forged or disordered export scope accepted: %v", chain)
		}
	}
	if chain, err := boundMCPExportScope("alpha", []string{"session:session-1", "workspace:alpha", "user_global:user-a"}); err != nil || len(chain) != 3 {
		t.Fatalf("bound scope rejected: %v, %v", chain, err)
	}
}
