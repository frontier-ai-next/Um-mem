package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	ummem "github.com/frontier-ai-next/Um-mem"
	"github.com/frontier-ai-next/Um-mem/internal/blockcatalog"
	"github.com/frontier-ai-next/Um-mem/internal/catalog"
	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/frontier-ai-next/Um-mem/internal/localservice"
	"github.com/frontier-ai-next/Um-mem/internal/mcpserver"
	"github.com/frontier-ai-next/Um-mem/internal/mcpsettings"
	"github.com/frontier-ai-next/Um-mem/internal/prospective"
	"github.com/frontier-ai-next/Um-mem/internal/protocolstdout"
	"github.com/frontier-ai-next/Um-mem/internal/telemetry"
	"github.com/frontier-ai-next/Um-mem/memory"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] != "dashboard" && os.Args[1] != "install-mcp" &&
		os.Args[1] != "mcp-status" && commandBackend(os.Args[1:]) == "mempalace" {
		if err := protocolstdout.Reserve(); err != nil {
			fmt.Fprintln(os.Stderr, "um-memory:", err)
			os.Exit(1)
		}
	}
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "um-memory:", err)
		os.Exit(1)
	}
}

func commandBackend(args []string) string {
	selected := ""
	for index, arg := range args {
		if arg == "--backend" && index+1 < len(args) {
			selected = args[index+1]
		}
		if strings.HasPrefix(arg, "--backend=") {
			selected = strings.TrimPrefix(arg, "--backend=")
		}
	}
	return selected
}

func readJSONFile(path string, target any) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("JSON file path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 4<<20))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("JSON file must contain exactly one value")
	}
	return nil
}

type scopeFlag struct{ values []string }

func (flag *scopeFlag) String() string {
	if len(flag.values) == 0 {
		return "workspace"
	}
	return flag.values[len(flag.values)-1]
}

func (flag *scopeFlag) Set(value string) error {
	flag.values = append(flag.values, value)
	return nil
}

func run(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("command required: dashboard, install-mcp, mcp-status, catalog, health, profile-report, rebuild-indexes, block-map, memory-map, obsidian-export, expand-block, navigation-envelope, prospective-create, prospective-get, prospective-transition, prospective-apply, prospective-candidates, prospective-intervention, prospective-telemetry, record-decision, record-usage, decision-trace, record-outcome-credit, outcome-credits, operator-report, observe-runtime-cost, explain-recall, deep-read, erase, pending-topology-evolutions, abort-topology-evolution, resume-topology-evolution, split-block, merge-blocks, observe-maintenance, add-edge, export-graph, create-block, amend-block, convert-block-to-index, protect-block, repair-protected-block-parent, archive-block, expire-due-blocks, revive-block, archive-session-block, revive-session-block, set-session-block-expiry, move-block, delete-block, forget, deprecate, rollback, amend, verification-subject, promote, consolidate-observations, commit-working-checkpoint, remember-candidate, operation-status, projection-barrier, put-root, put-successor, read-record, recall, recent, list-records or reconcile")
	}
	command := args[0]
	switch command {
	case "dashboard", "install-mcp", "mcp-status", "catalog", "health", "profile-report", "rebuild-indexes", "block-map", "memory-map", "obsidian-export", "export-obsidian", "expand-block", "navigation-envelope", "prospective-create", "prospective-get", "prospective-transition", "prospective-apply", "prospective-candidates", "prospective-intervention", "prospective-telemetry", "record-decision", "record-usage", "decision-trace", "record-outcome-credit", "outcome-credits", "operator-report", "observe-runtime-cost", "explain-recall", "deep-read", "erase", "pending-topology-evolutions", "abort-topology-evolution", "resume-topology-evolution", "split-block", "merge-blocks", "observe-maintenance", "add-edge", "export-graph", "create-block", "amend-block", "convert-block-to-index", "protect-block", "repair-protected-block-parent", "archive-block", "expire-due-blocks", "revive-block", "archive-session-block", "revive-session-block", "set-session-block-expiry", "move-block", "delete-block", "forget", "deprecate", "rollback", "amend", "verification-subject", "promote", "consolidate-observations", "commit-working-checkpoint", "remember-candidate", "operation-status", "projection-barrier", "put-root", "put-successor", "read-record", "recall", "recent", "list-records", "reconcile":
	default:
		return fmt.Errorf("unsupported command %q", command)
	}
	flags := flag.NewFlagSet("um-memory "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	repo := flags.String("repo-root", "", "repository root containing UM-MEM data")
	workspace := flags.String("workspace", "", "workspace ID")
	profile := flags.String("profile", "default", "profile ID")
	backend := flags.String("backend", "", "selected native backend")
	mcpHost := flags.String("host", "", "MCP host: claude, codex or hermes")
	settingsPath := flags.String("settings-path", "", "host settings file")
	mcpExecutable := flags.String("mcp-executable", "", "um-memory-mcp executable")
	stopDashboard := flags.Bool("stop", false, "stop the dashboard capability")
	noBrowser := flags.Bool("no-browser", false, "print the dashboard URL without opening a browser")
	baseURL := flags.String("base-url", os.Getenv("UM_MEM_HINDSIGHT_BASE_URL"), "Hindsight service URL")
	zvecModelDir := flags.String("zvec-model-dir", os.Getenv("UM_MEM_ZVEC_MODEL_DIR"), "Zvec ONNXMiniLM model directory")
	miniLMModelDir := flags.String("minilm-model-dir", os.Getenv("UM_MEM_MINILM_MODEL_DIR"), "shared ONNXMiniLM model directory for local native backends")
	recordFile := flags.String("record-file", "", "canonical record JSON file")
	requestFile := flags.String("request-file", "", "standard candidate request JSON file")
	operationID := flags.String("operation-id", "", "idempotent candidate operation ID")
	blockID := flags.String("block", "", "logical block ID")
	relatedCursor := flags.String("related-cursor", "", "related-edge pagination cursor")
	action := flags.String("action", "", "canonical successor action")
	rollbackTarget := flags.String("rollback-target", "", "rollback target record ID")
	recordID := flags.String("record-id", "", "exact record ID")
	obligationID := flags.String("obligation-id", "", "exact prospective obligation ID")
	decisionID := flags.String("decision-id", "", "exact memory decision ID")
	commitRevision := flags.Int64("commit-revision", 0, "expected native projection revision")
	timeoutSeconds := flags.Float64("timeout-seconds", 30, "projection barrier deadline in seconds")
	planFile := flags.String("plan-file", "", "recall plan JSON file")
	recentLimit := flags.Int("limit", 8, "recent record limit")
	recentSince := flags.String("since", "", "recent ISO-8601 lower bound")
	asOf := flags.String("as-of", "", "ISO-8601 time for explicit block expiry maintenance")
	reason := flags.String("reason", "", "reason for explicit block expiry maintenance")
	recentScope := &scopeFlag{}
	flags.Var(recentScope, "scope", "record scope or repeated Obsidian scope keys")
	recentScopeID := flags.String("scope-id", "", "recent record scope identity")
	recentBlock := flags.String("logical-block-id", "", "recent logical block filter")
	includeCandidates := flags.Bool("include-candidates", true, "include candidate records in recent results")
	pageCursor := flags.String("cursor", "", "record-page cursor")
	includeRetracted := flags.Bool("include-retracted", false, "include noncurrent record revisions in list-records")
	mapDepth := flags.Int("depth", 1, "block-map tree depth")
	mapIncludeMeta := flags.Bool("include-meta", false, "include protected catalog taxonomy in block-map")
	mapChildLimit := flags.Int("child-limit", 50, "block-map children per branch")
	mapOutputPath := flags.String("output-path", "", "memory-map HTML destination")
	vaultDir := flags.String("vault-dir", "", "Obsidian vault destination")
	projectTitle := flags.String("project-title", "", "Obsidian workspace title")
	scopeChain := flags.String("scope-chain", "", "comma-separated exact export scope chain")
	apply := flags.Bool("apply", false, "retry pending native projections")
	dryRun := flags.Bool("dry-run", false, "inspect pending projections without retrying")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	expandLimit := 50
	flags.Visit(func(selected *flag.Flag) {
		if selected.Name == "limit" {
			expandLimit = *recentLimit
		}
	})
	if *apply && *dryRun {
		return errors.New("--apply and --dry-run are mutually exclusive")
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	if command == "profile-report" {
		if strings.TrimSpace(*repo) == "" {
			return errors.New("--repo-root is required for profile-report")
		}
		limit := 10_000
		flags.Visit(func(selected *flag.Flag) {
			if selected.Name == "limit" {
				limit = *recentLimit
			}
		})
		report, err := telemetry.ProfileReport(*repo, limit)
		if err != nil {
			return err
		}
		return encoder.Encode(report)
	}
	if command == "install-mcp" || command == "mcp-status" {
		if *mcpHost == "" {
			return errors.New("--host is required")
		}
		if *settingsPath == "" {
			var err error
			*settingsPath, err = mcpsettings.DefaultSettingsPath(*mcpHost)
			if err != nil {
				return err
			}
		}
	}
	if command == "mcp-status" {
		return encoder.Encode(mcpsettings.Status(*mcpHost, *settingsPath))
	}
	if command == "install-mcp" {
		if *repo == "" {
			var err error
			*repo, err = mcpsettings.DefaultRepoRoot()
			if err != nil {
				return err
			}
		}
		if *workspace == "" {
			*workspace = strings.TrimSpace(os.Getenv("UM_MEM_WORKSPACE"))
			if *workspace == "" {
				*workspace = "default"
			}
		}
		if *backend == "" {
			*backend = "mempalace"
		}
	}
	if strings.TrimSpace(*repo) == "" || strings.TrimSpace(*workspace) == "" || strings.TrimSpace(*profile) == "" {
		return errors.New("--repo-root, --workspace and --profile are required")
	}
	if command == "install-mcp" {
		executable := *mcpExecutable
		if executable == "" {
			var err error
			executable, err = siblingExecutable("um-memory-mcp")
			if err != nil {
				return err
			}
		}
		config, err := mcpsettings.NewServerConfig(executable, mcpsettings.Identity{
			RepoRoot: *repo, WorkspaceID: *workspace, ProfileID: *profile, Backend: *backend})
		if err != nil {
			return err
		}
		if err := mcpsettings.Install(*mcpHost, *settingsPath, config); err != nil {
			return err
		}
		return encoder.Encode(mcpsettings.Status(*mcpHost, *settingsPath))
	}
	if command == "dashboard" {
		if *backend == "" {
			return errors.New("--backend is required for the dashboard")
		}
		result, err := dashboardCommand(ctx, localservice.Identity{RepoRoot: *repo,
			WorkspaceID: *workspace, ProfileID: *profile, Backend: *backend},
			*stopDashboard, !*noBrowser)
		if result != nil {
			if encodeErr := encoder.Encode(result); encodeErr != nil {
				return encodeErr
			}
		}
		if err != nil {
			return err
		}
		if result == nil || result["ok"] != true {
			return errors.New("dashboard is unavailable")
		}
		return nil
	}
	if command == "catalog" {
		blocks, err := blockcatalog.Load(blockcatalog.Options{RepoRoot: *repo, WorkspaceID: *workspace,
			ProfileID: *profile, Strict: true, IncludeWorkspace: true})
		if err != nil {
			return err
		}
		compiled, err := memory.CompileSemanticCatalog(blocks)
		if err != nil {
			return err
		}
		return encoder.Encode(map[string]any{"revision_id": compiled.RevisionID, "blocks": blocks})
	}
	if *backend == "" {
		return errors.New("--backend is required for canonical operations")
	}
	selected, err := engine.Open(engine.Config{RepoRoot: *repo, WorkspaceID: *workspace,
		ProfileID: *profile, BackendID: *backend, HindsightBaseURL: *baseURL,
		HindsightAPIKey: os.Getenv("UM_MEM_HINDSIGHT_API_KEY"), MiniLMModelDir: *miniLMModelDir, ZvecModelDir: *zvecModelDir})
	if err != nil {
		return err
	}
	defer selected.Close()
	switch command {
	case "observe-runtime-cost":
		var request ummem.RuntimeCostObservation
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		result, err := (&ummem.Client{Engine: selected}).ObserveRuntimeCost(ctx, request)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "erase":
		var request memory.MemoryEraseRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		result, err := selected.Erase(ctx, request)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "deep-read":
		var request mcpserver.DeepReadRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		result, err := mcpserver.DeepReadPayload(ctx, selected, request, *workspace, *backend)
		if err != nil {
			return err
		}
		return encoder.Encode(result.Mapping())
	case "explain-recall":
		var request mcpserver.ExplainRecallRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		result, err := mcpserver.ExplainRecallPayload(ctx, selected, request, *workspace, *backend,
			[]string{"workspace:" + *workspace}, request.RunID)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "record-decision":
		var event memory.MemoryDecisionEvent
		if err := readJSONFile(*requestFile, &event); err != nil {
			return err
		}
		result, err := selected.RecordMemoryDecision(ctx, event)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "record-usage":
		var event memory.MemoryUsageEvent
		if err := readJSONFile(*requestFile, &event); err != nil {
			return err
		}
		result, err := selected.RecordMemoryUsageEvent(ctx, event)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "decision-trace":
		result, err := selected.MemoryDecisionTrace(ctx, *decisionID)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "record-outcome-credit":
		var event memory.OutcomeCreditEvent
		if err := readJSONFile(*requestFile, &event); err != nil {
			return err
		}
		result, err := selected.RecordOutcomeCredit(ctx, event)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "outcome-credits":
		result, err := selected.OutcomeCredits(ctx, *decisionID)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "operator-report":
		result, err := selected.MemoryOperatorReport(ctx, *decisionID)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "prospective-create":
		var request struct {
			Obligation     memory.ProspectiveObligation `json:"obligation"`
			EventID        string                       `json:"event_id"`
			IdempotencyKey string                       `json:"idempotency_key"`
		}
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		result, err := selected.CreateProspectiveObligation(ctx, request.Obligation, request.EventID, request.IdempotencyKey)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "prospective-get":
		value, err := selected.GetProspectiveObligation(ctx, *obligationID)
		if err != nil {
			return err
		}
		return encoder.Encode(value)
	case "prospective-transition", "prospective-apply":
		var request struct {
			ObligationID   string                           `json:"obligation_id"`
			Event          memory.ProspectiveLifecycleEvent `json:"event"`
			TriggerContext *prospective.FireContext         `json:"trigger_context"`
		}
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		var result any
		if command == "prospective-transition" {
			result, err = selected.TransitionProspectiveObligation(ctx, request.ObligationID, request.Event)
		} else {
			result, err = selected.ApplyProspectiveLifecycleEvent(ctx, request.ObligationID, request.Event, request.TriggerContext)
		}
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "prospective-candidates":
		var request prospective.Context
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		result, err := selected.ProspectiveTriggerCandidates(ctx, request, expandLimit)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "prospective-intervention":
		value, err := selected.ProspectiveIntervention(ctx, *obligationID)
		if err != nil {
			return err
		}
		return encoder.Encode(map[string]any{"obligation_id": *obligationID, "text": value, "host_action_authorized": false})
	case "prospective-telemetry":
		limit := 256
		flags.Visit(func(selected *flag.Flag) {
			if selected.Name == "limit" {
				limit = *recentLimit
			}
		})
		result, err := selected.ProspectiveTelemetryReport(ctx, limit)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "rebuild-indexes":
		payload, err := selected.RebuildIndexes(ctx)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "health":
		payload, err := selected.Health(ctx)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "block-map":
		roots, err := selected.BlockMap(ctx, *mapDepth, *mapIncludeMeta, *mapChildLimit)
		if err != nil {
			return err
		}
		return encoder.Encode(map[string]any{"ok": true, "workspace_id": *workspace,
			"backend": *backend, "roots": roots})
	case "memory-map":
		payload, err := selected.WriteMemoryMap(ctx, *mapOutputPath)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "export-obsidian", "obsidian-export":
		if command == "obsidian-export" && *vaultDir == "" {
			return errors.New("--vault-dir is required for obsidian-export")
		}
		if *scopeChain != "" && len(recentScope.values) > 0 {
			return errors.New("--scope and --scope-chain cannot be combined")
		}
		chain := []string{}
		if *scopeChain != "" {
			for _, key := range strings.Split(*scopeChain, ",") {
				key = strings.TrimSpace(key)
				if key == "" {
					return errors.New("scope chain contains an empty key")
				}
				chain = append(chain, key)
			}
		}
		if len(recentScope.values) > 0 {
			chain = append(chain, recentScope.values...)
		}
		payload, err := selected.ExportObsidian(ctx, *vaultDir, *projectTitle, chain)
		if err != nil {
			return err
		}
		payload["workspace"] = *workspace
		return encoder.Encode(payload)
	case "expand-block":
		page, err := selected.ExpandBlock(ctx, *blockID, expandLimit, *pageCursor)
		if err != nil {
			return err
		}
		page["ok"], page["workspace_id"], page["backend"] = true, *workspace, *backend
		return encoder.Encode(page)
	case "navigation-envelope":
		payload, err := selected.NavigationEnvelope(ctx, *blockID, expandLimit,
			*pageCursor, *relatedCursor)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "pending-topology-evolutions":
		pending, err := selected.PendingTopologyEvolutions(ctx)
		if err != nil {
			return err
		}
		return encoder.Encode(map[string]any{"ok": true, "workspace_id": *workspace,
			"pending": pending})
	case "abort-topology-evolution":
		var request struct {
			OperationID string `json:"operation_id"`
			Reason      string `json:"reason"`
		}
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.AbortTopologyEvolution(ctx, request.OperationID, request.Reason)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "resume-topology-evolution":
		var request struct {
			OperationID string `json:"operation_id"`
		}
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.ResumeTopologyEvolution(ctx, request.OperationID)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "split-block":
		var request engine.SplitBlockRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.SplitBlock(ctx, request)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "merge-blocks":
		var request engine.MergeBlocksRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.MergeBlocks(ctx, request)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "observe-maintenance":
		var request catalog.AddObservationRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.ObserveMaintenance(ctx, request)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "add-edge":
		var request engine.AddEdgeRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.AddEdge(ctx, request)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "export-graph":
		payload, err := selected.ExportGraph(ctx)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "create-block":
		var request engine.CreateBlockRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.CreateBlock(ctx, request)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "amend-block":
		var request engine.AmendBlockRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.AmendBlock(ctx, request)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "convert-block-to-index":
		var request engine.ConvertBlockToIndexRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.ConvertBlockToIndex(ctx, request)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "archive-block":
		var request engine.BlockTransitionRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.ArchiveBlock(ctx, request)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "expire-due-blocks":
		payload, err := selected.ExpireDueBlocks(ctx, *asOf, *reason)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "repair-protected-block-parent":
		var request engine.RepairProtectedBlockParentRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.RepairProtectedBlockParent(ctx, request)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "archive-session-block", "revive-session-block", "set-session-block-expiry":
		var request engine.SessionBlockRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		var payload map[string]any
		var err error
		switch command {
		case "archive-session-block":
			payload, err = selected.ArchiveSessionBlock(ctx, request)
		case "revive-session-block":
			payload, err = selected.ReviveSessionBlock(ctx, request)
		default:
			payload, err = selected.SetSessionBlockExpiry(ctx, request)
		}
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "protect-block":
		var request engine.BlockTransitionRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.ProtectBlock(ctx, request)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "revive-block":
		var request engine.BlockTransitionRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.ReviveBlock(ctx, request)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "move-block":
		var request engine.MoveBlockRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.MoveBlock(ctx, request)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "delete-block":
		var request engine.DeleteBlockRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.DeleteBlock(ctx, request)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "forget":
		var request engine.ForgetRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.Forget(ctx, request)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "deprecate":
		var request engine.DeprecateRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.Deprecate(ctx, request)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "rollback":
		var request engine.RollbackRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.Rollback(ctx, request)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "amend":
		var request engine.AmendRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.Amend(ctx, request)
		if err != nil {
			return err
		}
		payload["backend"] = *backend
		return encoder.Encode(payload)
	case "verification-subject":
		var request engine.VerificationSubjectRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		subject, err := selected.VerificationSubject(ctx, request)
		if err != nil {
			return err
		}
		return encoder.Encode(map[string]any{"ok": true, "subject": subject,
			"workspace_id": *workspace, "backend": *backend})
	case "promote":
		var request engine.PromoteRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.Promote(ctx, request)
		if err != nil {
			return err
		}
		payload["workspace_id"] = *workspace
		return encoder.Encode(payload)
	case "consolidate-observations":
		var request engine.ConsolidateObservationsRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.ConsolidateObservations(ctx, request)
		if err != nil {
			return err
		}
		payload["workspace_id"], payload["backend"] = *workspace, *backend
		return encoder.Encode(payload)
	case "commit-working-checkpoint":
		var request engine.CommitWorkingCheckpointRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		payload, err := selected.CommitWorkingCheckpoint(ctx, request)
		if err != nil {
			return err
		}
		payload["workspace_id"], payload["backend"] = *workspace, *backend
		return encoder.Encode(payload)
	case "operation-status":
		status, err := selected.OperationStatus(ctx, *operationID)
		if err != nil {
			return err
		}
		return encoder.Encode(status)
	case "projection-barrier":
		result, err := selected.ProjectionBarrier(ctx, *recordID, *commitRevision, *timeoutSeconds)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "remember-candidate":
		var request engine.CandidateRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		result, err := selected.RememberCandidate(ctx, request)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	case "put-root":
		if strings.TrimSpace(*blockID) == "" {
			return errors.New("--block is required for put-root")
		}
		var record memory.MemoryRecord
		if err := readJSONFile(*recordFile, &record); err != nil {
			return err
		}
		if err := selected.BindRootRecord(ctx, &record, *blockID); err != nil {
			return err
		}
		receipt, err := selected.PutRoot(ctx, record)
		if err != nil {
			return err
		}
		return encoder.Encode(receipt)
	case "put-successor":
		if strings.TrimSpace(*blockID) == "" || strings.TrimSpace(*action) == "" {
			return errors.New("--block and --action are required for put-successor")
		}
		var record memory.MemoryRecord
		if err := readJSONFile(*recordFile, &record); err != nil {
			return err
		}
		if err := selected.BindSuccessorRecord(ctx, &record, *blockID); err != nil {
			return err
		}
		receipt, err := selected.PutSuccessor(ctx, record, *action, *rollbackTarget)
		if err != nil {
			return err
		}
		return encoder.Encode(receipt)
	case "read-record":
		if strings.TrimSpace(*recordID) == "" {
			return errors.New("--record-id is required for read-record")
		}
		record, err := selected.ReadStored(ctx, *recordID)
		if err != nil {
			return err
		}
		return encoder.Encode(record)
	case "recall":
		var plan memory.RecallPlan
		if err := readJSONFile(*planFile, &plan); err != nil {
			return err
		}
		records, err := selected.Recall(ctx, plan)
		if err != nil {
			return err
		}
		return encoder.Encode(records)
	case "recent":
		records, err := selected.Recent(ctx, engine.RecentRequest{Limit: *recentLimit,
			Since: *recentSince, Scope: recentScope.String(), ScopeID: *recentScopeID,
			LogicalBlockID: *recentBlock, IncludeCandidates: *includeCandidates})
		if err != nil {
			return err
		}
		return encoder.Encode(records)
	case "list-records":
		limit := *recentLimit
		setLimit := false
		flags.Visit(func(selected *flag.Flag) {
			if selected.Name == "limit" {
				setLimit = true
			}
		})
		if !setLimit {
			limit = 32
		}
		page, err := selected.ListRecords(ctx, engine.ListRecordsRequest{Scope: recentScope.String(),
			ScopeID: *recentScopeID, LogicalBlockID: *recentBlock, Limit: limit,
			Cursor: *pageCursor, IncludeRetracted: *includeRetracted})
		if err != nil {
			return err
		}
		return encoder.Encode(page)
	case "reconcile":
		report, err := selected.Reconcile(ctx, !*apply)
		if err != nil {
			return err
		}
		if err := encoder.Encode(report); err != nil {
			return err
		}
		if *apply && report.Failures > 0 {
			return errors.New("one or more native projections remain unresolved")
		}
		return nil
	}
	return errors.New("unreachable command")
}
