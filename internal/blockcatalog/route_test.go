package blockcatalog

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

func TestAgentWriteRoutingMatchesPython(t *testing.T) {
	blocks, err := Load(Options{RepoRoot: t.TempDir(), WorkspaceID: "alpha", ProfileID: "team", Strict: true, IncludeWorkspace: true})
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("../../memory/testdata/route_decisions_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string               `json:"name"`
		Request  memory.WriteProposal `json:"request"`
		Decision map[string]any       `json:"decision"`
	}
	if err := json.Unmarshal(fixture, &cases); err != nil {
		t.Fatal(err)
	}
	for _, entry := range cases {
		t.Run(entry.Name, func(t *testing.T) {
			actual, err := memory.RouteMemoryWrite(entry.Request, blocks)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(actual)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			// Each fresh profile catalog creates a distinct protected root UUID.
			// The semantic hash is covered by the shared-catalog fixture tests.
			if got["catalog_revision_id"] == "" {
				t.Fatal("Go route lost the bound catalog revision")
			}
			delete(got, "catalog_revision_id")
			delete(entry.Decision, "catalog_revision_id")
			if !reflect.DeepEqual(got, entry.Decision) {
				t.Fatalf("Go route differs from Python: got=%+v want=%+v", got, entry.Decision)
			}
		})
	}
}
