package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/frontier-ai-next/Um-mem/internal/mcpserver"
	"github.com/frontier-ai-next/Um-mem/internal/protocolstdout"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "um-memory-mcp:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("um-memory-mcp", flag.ContinueOnError)
	repo := flags.String("repo-root", "", "repository root containing UM-MEM data")
	workspace := flags.String("workspace", "", "workspace ID")
	profile := flags.String("profile", "default", "profile ID")
	backend := flags.String("backend", "", "selected native backend")
	baseURL := flags.String("base-url", os.Getenv("UM_MEM_HINDSIGHT_BASE_URL"), "Hindsight service URL")
	zvecModelDir := flags.String("zvec-model-dir", os.Getenv("UM_MEM_ZVEC_MODEL_DIR"), "Zvec ONNXMiniLM model directory")
	miniLMModelDir := flags.String("minilm-model-dir", os.Getenv("UM_MEM_MINILM_MODEL_DIR"), "shared ONNXMiniLM model directory for local native backends")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if strings.TrimSpace(*repo) == "" || strings.TrimSpace(*workspace) == "" ||
		strings.TrimSpace(*profile) == "" || strings.TrimSpace(*backend) == "" {
		return fmt.Errorf("--repo-root, --workspace, --profile and --backend are required")
	}
	if *backend == "mempalace" {
		if err := protocolstdout.Reserve(); err != nil {
			return err
		}
	}
	selected, err := engine.Open(engine.Config{RepoRoot: *repo, WorkspaceID: *workspace,
		ProfileID: *profile, BackendID: *backend, HindsightBaseURL: *baseURL,
		HindsightAPIKey: os.Getenv("UM_MEM_HINDSIGHT_API_KEY"), MiniLMModelDir: *miniLMModelDir, ZvecModelDir: *zvecModelDir})
	if err != nil {
		return err
	}
	defer selected.Close()
	return mcpserver.New(selected, *workspace, *backend).Run(ctx, &mcp.StdioTransport{})
}
