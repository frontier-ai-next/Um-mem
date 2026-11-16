package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// This exercises the real process boundary: embedded Chroma writes HNSW
// integrity diagnostics through C++ stdout when a Python palace is reopened.
func TestMemPalaceCLIReservesProtocolStdout(t *testing.T) {
	root := os.Getenv("UM_MEM_TEST_PYTHON_PALACE_ROOT")
	modelDir := os.Getenv("UM_MEM_TEST_MINILM_DIR")
	if root == "" || modelDir == "" {
		t.Skip("Python MemPalace and MiniLM fixtures are unavailable")
	}
	plan := memory.NewRecallPlan("blue comet protocol")
	plan.WorkspaceID, plan.ProfileID = "alpha", "default"
	plan.SearchAll = true
	plan.ScopeChain = []string{"workspace:alpha"}
	plan.IncludeCandidates = true
	planPath := filepath.Join(t.TempDir(), "plan.json")
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(planPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "um-memory")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Go CLI: %v\n%s", err, output)
	}
	command := exec.CommandContext(ctx, binary, "recall", "--repo-root", root, "--workspace", "alpha",
		"--profile", "default", "--backend", "mempalace", "--minilm-model-dir", modelDir,
		"--plan-file", planPath)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("Go MemPalace CLI: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	var hits []memory.MemoryRecord
	if err := json.Unmarshal(stdout.Bytes(), &hits); err != nil {
		t.Fatalf("MemPalace CLI stdout contains non-JSON diagnostics: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	found := false
	for _, hit := range hits {
		found = found || hit.BlockID == "mem_c3a6452e2bd27eca"
	}
	if !found {
		t.Fatalf("MemPalace CLI missed Python record: %+v", hits)
	}
}

func TestCommandBackendUsesFinalFlag(t *testing.T) {
	if actual := commandBackend([]string{"recall", "--backend", "hindsight", "--backend=mempalace"}); actual != "mempalace" {
		t.Fatalf("backend pre-scan differs from flag parser: %q", actual)
	}
}
