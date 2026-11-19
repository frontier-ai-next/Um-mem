package main

import (
	"bufio"
	"bytes"
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
	// Windows tooling (Windows PowerShell 5 `Set-Content -Encoding utf8`, some
	// editors) prepends a UTF-8 BOM, which encoding/json rejects. Strip it so a
	// hand-written request file is accepted on the platform the CLI ships for.
	reader := bufio.NewReader(file)
	if head, peekErr := reader.Peek(3); peekErr == nil && bytes.Equal(head, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = reader.Discard(3)
	}
	decoder := json.NewDecoder(io.LimitReader(reader, 4<<20))
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

// nameRevisionOutcome gives a mutation receipt the two names a host adapter
// needs: `record_id` is the record as it exists now, `revision_id` is the
// revision that operation wrote. The engine reports those under `new_id` /
// `new_revision` for amend and under `block_id` / `revision_id` for forget,
// which reads as "the record changed its id" to any caller.
func nameRevisionOutcome(payload map[string]any, memoryID string) map[string]any {
	if revision, ok := payload["new_revision"].(string); ok && revision != "" {
		payload["revision_id"] = revision
	}
	if current, ok := payload["new_id"].(string); ok && current != "" {
		payload["record_id"] = current
	} else if trimmed := strings.TrimSpace(memoryID); trimmed != "" {
		payload["record_id"] = trimmed
	}
	return payload
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

const usageText = `um-memory — локальный рантайм памяти для агентов.

Использование:
  um-memory <command> --repo-root <store> --workspace <id> [--profile default] --backend <backend> [flags]

Чтение:
  health                     состояние бэкенда и проекции
  catalog                    каталог логических блоков
  block-map --depth N        дерево блоков
  memory-map --output-path   статичная HTML-карта
  recent --limit N --scope workspace|user_global --scope-id <id>
  list-records --scope <s> --scope-id <id> [--limit N] [--cursor <c>]
  read-record --record-id <id>
  recall --plan-file <json>
  explain-recall --request-file <json>
  neighbors --record-id <id> [--depth 1..3] [--edge-types a,b] [--neighbor-limit N]
  export-graph               записи и рёбра целиком

Запись:
  remember-candidate --request-file <json>
  amend | forget | deprecate | rollback --request-file <json>
  add-edge --request-file <json>          src_id, dst_id, edge_type, evidence_refs
  promote | consolidate-observations --request-file <json>

Обслуживание:
  reconcile [--dry-run|--apply]           повтор нативных проекций
  dashboard [--no-browser|--stop]         локальная панель
  agent-topics --request-file FILE       поиск или создание смысловой ветки
  install-mcp --host claude|codex|hermes  регистрация MCP-сервера
  mcp-status --host <h>
  profile-report | operator-report | observe-runtime-cost | rebuild-indexes

Все команды печатают JSON в stdout. Общие флаги: --repo-root, --workspace,
--profile, --backend; для user_global записей нужен --user-id (в запросе поле user_id).
`

func run(ctx context.Context, args []string, output io.Writer) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Fprint(output, usageText)
		return nil
	}
	if len(args) == 0 {
		return errors.New("command required: run `um-memory --help` for the command list")
	}
	command := args[0]
	switch command {
	case "dsh-focus", "agent-topics":
	case "dashboard", "install-mcp", "mcp-status", "catalog", "health", "profile-report", "rebuild-indexes", "block-map", "memory-map", "obsidian-export", "export-obsidian", "expand-block", "navigation-envelope", "prospective-create", "prospective-get", "prospective-transition", "prospective-apply", "prospective-candidates", "prospective-intervention", "prospective-telemetry", "record-decision", "record-usage", "decision-trace", "record-outcome-credit", "outcome-credits", "operator-report", "observe-runtime-cost", "explain-recall", "deep-read", "erase", "pending-topology-evolutions", "abort-topology-evolution", "resume-topology-evolution", "split-block", "merge-blocks", "observe-maintenance", "add-edge", "export-graph", "create-block", "amend-block", "convert-block-to-index", "protect-block", "repair-protected-block-parent", "archive-block", "expire-due-blocks", "revive-block", "archive-session-block", "revive-session-block", "set-session-block-expiry", "move-block", "delete-block", "forget", "deprecate", "rollback", "amend", "verification-subject", "promote", "consolidate-observations", "commit-working-checkpoint", "remember-candidate", "operation-status", "projection-barrier", "put-root", "put-successor", "read-record", "recall", "recent", "list-records", "neighbors", "auto-link", "memory-stats", "stable-id", "promotion-candidates", "contradictions", "consolidation", "export-jsonl", "import-jsonl", "approve", "graph-report", "weave-blocks", "purge", "reconcile":
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
	edgeTypes := flags.String("edge-types", "", "comma-separated edge types for neighbors")
	neighborLimit := flags.Int("neighbor-limit", 12, "neighbors result limit")
	autoLinkOn := flags.Bool("auto-link", true, "attach a related_to edge after a candidate write")
	linkThreshold := flags.Float64("link-threshold", 0.12, "minimum lexical similarity for auto-link")
	conflictThreshold := flags.Float64("conflict-threshold", 0.55, "minimum similarity for a contradiction pair")
	confirmConsolidation := flags.Bool("confirm-consolidation", false, "required together with --apply: retirement is irreversible from the caller side")
	currentOnly := flags.Bool("current-only", false, "export only records that are still in force")
	graphHTML := flags.String("html", "", "write a standalone web view of the graph")
	purgeIDs := flags.String("purge-ids", "", "comma separated record or revision ids to purge")
	purgeKinds := flags.String("purge-kinds", "", "comma separated kinds to purge, e.g. episode")
	purgeBlocks := flags.String("purge-blocks", "", "comma separated blocks to purge")
	purgeConfirm := flags.Bool("purge-confirm", false, "required together with --apply")
	purgeShred := flags.Bool("purge-shred", false, "delete the old store directory after a successful swap")
	approverFlag := flags.String("approver", "", "operator identity for an approval")
	approvalNote := flags.String("note", "", "why the record is approved")
	facets := &scopeFlag{}
	flags.Var(facets, "facet", "recall facet filter as key=value, repeatable")
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
	if command == "dsh-focus" {
		var request dshFocusRequest
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		result, err := dshFocus(ctx, selected, request)
		if err != nil {
			return err
		}
		return encoder.Encode(result)
	}
	switch command {
	case "agent-topics":
		var request struct {
			Query string   `json:"query"`
			Limit int      `json:"limit"`
			Path  []string `json:"path"`
			About string   `json:"about"`
		}
		if err := readJSONFile(*requestFile, &request); err != nil {
			return err
		}
		if request.Path == nil {
			topics, err := selected.AgentTopics(request.Query, request.Limit)
			if err != nil {
				return err
			}
			return encoder.Encode(topics)
		}
		topic, err := selected.EnsureAgentTopic(ctx, request.Path, request.About)
		if err != nil {
			return err
		}
		return encoder.Encode(topic)
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
		return encoder.Encode(nameRevisionOutcome(payload, request.MemoryID))
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
		return encoder.Encode(nameRevisionOutcome(payload, request.MemoryID))
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
		return encoder.Encode(nameRevisionOutcome(payload, request.MemoryID))
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
		if *autoLinkOn && result.Saved && result.RecordID != "" {
			// Best-effort: a missing neighbour must never fail a durable write.
			if link, linkErr := selected.AutoLink(ctx, result.RecordID, *linkThreshold); linkErr == nil {
				result.AutoLink = link
			}
		}
		return encoder.Encode(result)
	case "auto-link":
		if strings.TrimSpace(*recordID) == "" {
			return errors.New("--record-id is required for auto-link")
		}
		payload, err := selected.AutoLink(ctx, *recordID, *linkThreshold)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "memory-stats":
		payload, err := selected.Stats(ctx)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
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
		// Reading by stable (root) id should land on the live revision; reading by
		// an explicit revision id stays exact, so audit reads keep working.
		if resolved, resolveErr := selected.StableResolve(ctx, *recordID); resolveErr == nil {
			stable, _ := resolved["stable_id"].(string)
			current, _ := resolved["record_id"].(string)
			if stable != "" && stable == *recordID && current != "" && current != *recordID {
				if latest, latestErr := selected.ReadStored(ctx, current); latestErr == nil {
					record, err = latest, nil
				}
			}
		}
		if err != nil {
			return err
		}
		if record == nil {
			return errors.New("record not found: " + *recordID)
		}
		// `block_id` on a stored record is the record identity, which reads as
		// the logical block to every caller. Publish both names explicitly so a
		// host does not have to know the internal naming.
		raw, err := json.Marshal(record)
		if err != nil {
			return err
		}
		payload := map[string]any{}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return err
		}
		payload["record_id"] = record.BlockID
		if record.BlockMembership != nil {
			payload["logical_block_id"] = record.BlockMembership.PrimaryBlockID
		} else {
			payload["logical_block_id"] = ""
		}
		if resolved, resolveErr := selected.StableResolve(ctx, record.BlockID); resolveErr == nil {
			payload["stable_id"] = resolved["stable_id"]
			payload["revisions"] = resolved["revisions"]
		}
		if withdrawn, werr := selected.WithdrawnIDs(ctx); werr == nil {
			payload["withdrawn"] = withdrawn[record.BlockID]
		}
		return encoder.Encode(payload)
	case "stable-id":
		if strings.TrimSpace(*recordID) == "" {
			return errors.New("--record-id is required for stable-id")
		}
		payload, err := selected.StableResolve(ctx, *recordID)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "neighbors":
		if strings.TrimSpace(*recordID) == "" {
			return errors.New("--record-id is required for neighbors")
		}
		types := make([]string, 0, 4)
		for _, value := range strings.Split(*edgeTypes, ",") {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				types = append(types, trimmed)
			}
		}
		payload, err := selected.Neighbors(ctx, engine.NeighborRequest{
			RecordID: *recordID, Depth: *mapDepth, EdgeTypes: types, Limit: *neighborLimit,
		})
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "recall":
		var plan memory.RecallPlan
		if err := readJSONFile(*planFile, &plan); err != nil {
			return err
		}
		for _, item := range facets.values {
			key, value, ok := strings.Cut(item, "=")
			if !ok || strings.TrimSpace(key) == "" {
				return errors.New("--facet must be key=value")
			}
			if plan.Facets == nil {
				plan.Facets = map[string][]string{}
			}
			plan.Facets[strings.TrimSpace(key)] = append(plan.Facets[strings.TrimSpace(key)], strings.TrimSpace(value))
		}
		records, err := selected.Recall(ctx, plan)
		if err != nil {
			return err
		}
		return encoder.Encode(records)
	case "promotion-candidates":
		payload, err := selected.PromotionCandidates(ctx, *neighborLimit)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "contradictions":
		payload, err := selected.Contradictions(ctx, *conflictThreshold, *neighborLimit)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "weave-blocks":
		payload, err := selected.WeaveBlocks(ctx, *conflictThreshold, *apply, *neighborLimit)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "purge":
		if len(strings.TrimSpace(*purgeIDs)) == 0 && len(strings.TrimSpace(*purgeKinds)) == 0 && len(strings.TrimSpace(*purgeBlocks)) == 0 {
			return errors.New("purge needs --record-id, --kind or --block")
		}
		request := engine.PurgeRequest{Apply: *apply, Confirm: *purgeConfirm, Shred: *purgeShred}
		for _, part := range strings.Split(*purgeIDs, ",") {
			if value := strings.TrimSpace(part); value != "" {
				request.RecordIDs = append(request.RecordIDs, value)
			}
		}
		for _, part := range strings.Split(*purgeKinds, ",") {
			if value := strings.TrimSpace(part); value != "" {
				request.Kinds = append(request.Kinds, value)
			}
		}
		for _, part := range strings.Split(*purgeBlocks, ",") {
			if value := strings.TrimSpace(part); value != "" {
				request.Blocks = append(request.Blocks, value)
			}
		}
		payload, err := selected.Purge(ctx, request)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "graph-report":
		if strings.TrimSpace(*graphHTML) != "" {
			payload, err := selected.GraphHTML(ctx, *graphHTML)
			if err != nil {
				return err
			}
			return encoder.Encode(payload)
		}
		payload, err := selected.GraphReport(ctx)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "import-jsonl":
		payload, err := selected.ImportJSONL(ctx, *mapOutputPath)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "approve":
		if strings.TrimSpace(*recordID) == "" {
			return errors.New("--record-id is required for approve")
		}
		approver := strings.TrimSpace(*approverFlag)
		payload, err := selected.Approve(ctx, *recordID, approver, *approvalNote)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "export-jsonl":
		payload, err := selected.ExportJSONL(ctx, *mapOutputPath, *currentOnly)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
	case "consolidation":
		if *apply && !*confirmConsolidation {
			return errors.New("consolidation --apply requires --confirm-consolidation: review the dry-run plan first")
		}
		payload, err := selected.Consolidation(ctx, *conflictThreshold, *apply && *confirmConsolidation, *neighborLimit)
		if err != nil {
			return err
		}
		return encoder.Encode(payload)
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
