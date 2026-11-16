package hosthook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/frontier-ai-next/Um-mem/internal/localservice"
)

func TestHermesShellHookInstallPreservesOtherSettings(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.yaml")
	initial := "# Keep this operator note\nmemory:\n  provider: hindsight\nhooks:\n  pre_llm_call:\n    - command: /other/hook\n      timeout: 3\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	identity := localservice.Identity{RepoRoot: t.TempDir(), WorkspaceID: "alpha",
		ProfileID: "team", Backend: "mnemosyne"}
	for range 2 {
		if _, err := InstallHermesShellHooks(home, executable, identity); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	if !strings.Contains(content, "# Keep this operator note") ||
		!strings.Contains(content, "provider: hindsight") ||
		!strings.Contains(content, "/other/hook") ||
		strings.Count(content, "--host hermes") != len(hermesRegistrations) {
		t.Fatalf("Hermes hook merge lost existing settings or duplicated owned hooks: %s", content)
	}
	status, err := HermesShellHookStatus(home)
	if err != nil || !status.Installed || len(status.InstalledEvents) != len(hermesRegistrations) {
		t.Fatalf("Hermes Go shell hooks were not detected: %+v %v", status, err)
	}
}
