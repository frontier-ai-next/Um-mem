package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/frontier-ai-next/Um-mem/internal/localservice"
	"github.com/frontier-ai-next/Um-mem/internal/scheduler"
	"github.com/frontier-ai-next/Um-mem/internal/servicepolicy"
	"github.com/frontier-ai-next/Um-mem/internal/servicequeue"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "um-memory-service:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("um-memory-service", flag.ContinueOnError)
	repo := flags.String("repo-root", "", "repository root containing UM-MEM data")
	workspace := flags.String("workspace", "", "workspace ID")
	profile := flags.String("profile", "default", "profile ID")
	backend := flags.String("backend", "", "selected native backend")
	ownerPID := flags.Int("owner-pid", 0, "process that launched the listener")
	baseURL := flags.String("base-url", os.Getenv("UM_MEM_HINDSIGHT_BASE_URL"), "Hindsight service URL")
	zvecModelDir := flags.String("zvec-model-dir", os.Getenv("UM_MEM_ZVEC_MODEL_DIR"), "Zvec ONNXMiniLM model directory")
	miniLMModelDir := flags.String("minilm-model-dir", os.Getenv("UM_MEM_MINILM_MODEL_DIR"), "shared ONNXMiniLM model directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*repo) == "" ||
		strings.TrimSpace(*workspace) == "" || strings.TrimSpace(*profile) == "" ||
		strings.TrimSpace(*backend) == "" || *ownerPID < 0 {
		return errors.New("--repo-root, --workspace, --profile and --backend are required with a non-negative --owner-pid")
	}
	selected, err := engine.Open(engine.Config{RepoRoot: *repo, WorkspaceID: *workspace,
		ProfileID: *profile, BackendID: *backend, HindsightBaseURL: *baseURL,
		HindsightAPIKey: os.Getenv("UM_MEM_HINDSIGHT_API_KEY"),
		MiniLMModelDir:  *miniLMModelDir, ZvecModelDir: *zvecModelDir})
	if err != nil {
		return err
	}
	defer selected.Close()
	identity := localservice.Identity{RepoRoot: *repo, WorkspaceID: *workspace,
		ProfileID: *profile, Backend: *backend}
	secrets, err := localservice.LoadOrCreateBootstrap(identity)
	if err != nil {
		return err
	}
	server, err := localservice.New(identity, secrets, localservice.EngineDispatcher(selected))
	if err != nil {
		return err
	}
	snapshot, err := servicepolicy.SnapshotFromBlocks(selected.Blocks())
	if err != nil {
		return err
	}
	policy, err := servicepolicy.NewStore(servicepolicy.Identity{RepoRoot: *repo,
		WorkspaceID: *workspace, ProfileID: *profile, Backend: *backend})
	if err != nil {
		return err
	}
	if _, err := policy.EnsureInitialized(snapshot); err != nil {
		return err
	}
	if err := server.ConfigurePolicy(policy); err != nil {
		return err
	}
	schedulerPath, identityDigest, err := localservice.SchedulerPath(identity)
	if err != nil {
		return err
	}
	queue, err := scheduler.Open(schedulerPath, identityDigest, *workspace, selected.PlanHostState)
	if err != nil {
		return err
	}
	defer queue.Close()
	if err := server.ConfigureScheduler(queue); err != nil {
		return err
	}
	workPath, err := localservice.WorkQueuePath(identity)
	if err != nil {
		return err
	}
	// Do not recover another listener's running work before this process owns
	// the registry and can run every queue handler.
	workQueue, err := servicequeue.Open(workPath, nil, false)
	if err != nil {
		return err
	}
	defer workQueue.Close()
	if err := server.ConfigureWorkQueue(workQueue); err != nil {
		return err
	}
	if err := server.ConfigurePanel(selected); err != nil {
		return err
	}
	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	_, done, err := server.StartListener(serviceCtx, *ownerPID)
	if err != nil {
		return err
	}
	background, err := startBackground(serviceCtx, selected, policy, queue, workQueue,
		func(recordID string) {
			server.PublishPanelEvent(localservice.ChangeEvent{Kind: "indexing",
				Operation: "indexing", OK: true, RecordID: recordID})
		})
	if err != nil {
		cancel()
		return errors.Join(err, <-done)
	}
	defer background.Close()
	server.SetBackgroundReady(true)
	defer server.SetBackgroundReady(false)
	go monitorIdle(serviceCtx, server, localServiceIdleTimeout())
	select {
	case listenerErr := <-done:
		return listenerErr
	case backgroundErr := <-background.loopDone:
		server.SetBackgroundReady(false)
		cancel()
		return errors.Join(backgroundErr, <-done)
	}
}

func localServiceIdleTimeout() time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(os.Getenv("UM_MEM_LOCAL_SERVICE_IDLE_SECONDS")))
	if err != nil || seconds == 0 {
		seconds = 300
	}
	if seconds < 1 {
		seconds = 1
	}
	return time.Duration(seconds) * time.Second
}

func monitorIdle(ctx context.Context, server *localservice.Server, timeout time.Duration) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check, cancel := context.WithTimeout(ctx, 2*time.Second)
			idle := server.IsIdle(check, timeout)
			cancel()
			if idle {
				server.BeginShutdown()
				return
			}
		}
	}
}
