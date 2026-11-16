// Package servicepolicy preserves the local listener's content-free policy
// bundle and session pins across Python and Go runtimes.
package servicepolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/internal/paths"
	"github.com/frontier-ai-next/Um-mem/memory"
)

const (
	activationRevision     = "activation:um_mem.memory.selection.MemorySelectionPolicy.eligible_for_overlay@1"
	workingContextRevision = "working_context:um_mem.memory.working_continuity.compile_continuation_packet@1"
	recallRevision         = "recall_operator:um_mem.memory.recall_plan.RecallPlan.compile@2"
	consolidationRevision  = "consolidation:um_mem.memory.consolidation.CanonicalConsolidationPipeline.propose@1"
	prospectiveRevision    = "prospective:um_mem.memory.prospective.evaluate_prospective_trigger@1"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,255}$`)

type Identity struct {
	RepoRoot    string
	WorkspaceID string
	ProfileID   string
	Backend     string
}

func (identity Identity) normalized() (Identity, error) {
	if strings.TrimSpace(identity.RepoRoot) == "" {
		return Identity{}, errors.New("service policy repo root is required")
	}
	root, err := filepath.Abs(strings.TrimSpace(identity.RepoRoot))
	if err != nil {
		return Identity{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Identity{}, err
	}
	workspace, err := paths.NormalizeWorkspaceID(identity.WorkspaceID)
	if err != nil || workspace == "" {
		return Identity{}, errors.New("service policy workspace identity is invalid")
	}
	profile := strings.TrimSpace(identity.ProfileID)
	if profile == "" {
		profile = "default"
	}
	backend := strings.ToLower(strings.TrimSpace(identity.Backend))
	if !identifierPattern.MatchString(profile) || !identifierPattern.MatchString(backend) {
		return Identity{}, errors.New("service policy profile or backend is invalid")
	}
	return Identity{RepoRoot: root, WorkspaceID: workspace, ProfileID: profile, Backend: backend}, nil
}

func sha(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func identityRecord(identity Identity) map[string]string {
	root := identity.RepoRoot
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
	}
	return map[string]string{"repo_root_sha256": sha(root),
		"workspace_id_sha256": sha(identity.WorkspaceID),
		"profile_id_sha256":   sha(identity.ProfileID), "backend": identity.Backend}
}

func StatePath(identity Identity) (string, error) {
	identity, err := identity.normalized()
	if err != nil {
		return "", err
	}
	encoded, err := jsoncompat.MarshalPython(identityRecord(identity), true)
	if err != nil {
		return "", err
	}
	return filepath.Join(identity.RepoRoot, ".UM-MEM", "memory", "service_policy_bundles",
		sha(string(encoded))+".json"), nil
}

type Snapshot []map[string]any

var contentKeys = map[string]bool{"content": true, "message": true, "messages": true,
	"prompt": true, "raw_input": true, "raw_output": true, "text": true, "transcript": true}

func safeValue(value any) (any, error) {
	switch item := value.(type) {
	case nil, string, bool, int, int64, json.Number:
		return item, nil
	case float64:
		if math.IsInf(item, 0) || math.IsNaN(item) {
			return nil, errors.New("catalog snapshot has a non-finite number")
		}
		return item, nil
	case []any:
		result := make([]any, len(item))
		for i, value := range item {
			validated, err := safeValue(value)
			if err != nil {
				return nil, err
			}
			result[i] = validated
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(item))
		for key, value := range item {
			if !identifierPattern.MatchString(key) || contentKeys[strings.ToLower(key)] {
				return nil, errors.New("catalog snapshot key is invalid or contains host content")
			}
			validated, err := safeValue(value)
			if err != nil {
				return nil, err
			}
			result[key] = validated
		}
		return result, nil
	default:
		return nil, fmt.Errorf("catalog snapshot value has unsupported type %T", value)
	}
}

func NormalizeSnapshot(rows []map[string]any) (Snapshot, error) {
	if len(rows) == 0 {
		return nil, errors.New("catalog snapshot must not be empty")
	}
	result := make(Snapshot, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		validated, err := safeValue(row)
		if err != nil {
			return nil, err
		}
		mapping := validated.(map[string]any)
		id, ok := mapping["block_id"].(string)
		if !ok || !identifierPattern.MatchString(id) || seen[id] {
			return nil, errors.New("catalog snapshot block ID is invalid or repeated")
		}
		seen[id] = true
		result = append(result, mapping)
	}
	sort.Slice(result, func(i, j int) bool { return result[i]["block_id"].(string) < result[j]["block_id"].(string) })
	return result, nil
}

func SnapshotFromBlocks(blocks []memory.Block) (Snapshot, error) {
	encoded, err := json.Marshal(blocks)
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	if err := json.Unmarshal(encoded, &rows); err != nil {
		return nil, err
	}
	return NormalizeSnapshot(rows)
}

func snapshotDigest(snapshot Snapshot) (string, error) {
	normalized, err := NormalizeSnapshot(snapshot)
	if err != nil {
		return "", err
	}
	encoded, err := jsoncompat.MarshalPython(normalized, true)
	if err != nil {
		return "", err
	}
	return sha(string(encoded)), nil
}

func CatalogRevision(snapshot Snapshot) (string, error) {
	digest, err := snapshotDigest(snapshot)
	if err != nil {
		return "", err
	}
	return "catalog:um_mem.memory.blocks.registry.load_memory_blocks@1:sha256:" + digest, nil
}

func BlockRevision(snapshot Snapshot) (string, error) {
	digest, err := snapshotDigest(snapshot)
	if err != nil {
		return "", err
	}
	return "blocks:um_mem.memory.blocks.registry@1:sha256:" + digest, nil
}

func ProjectionRevision(snapshot Snapshot) (string, error) {
	digest, err := snapshotDigest(snapshot)
	if err != nil {
		return "", err
	}
	return "projection:um_mem.memory.blocks.catalog@1:sha256:" + digest, nil
}

type Bundle struct {
	CatalogRevisionID           string `json:"catalog_revision_id"`
	ActivationRevisionID        string `json:"activation_revision_id"`
	WorkingContextRevisionID    string `json:"working_context_revision_id"`
	RecallOperatorRevisionID    string `json:"recall_operator_revision_id"`
	ConsolidationRevisionID     string `json:"consolidation_revision_id"`
	ProspectivePolicyRevisionID string `json:"prospective_policy_revision_id"`
	RevisionID                  string `json:"revision_id"`
}

func (bundle Bundle) revisionRecord() map[string]string {
	return map[string]string{"catalog_revision_id": bundle.CatalogRevisionID,
		"activation_revision_id":         bundle.ActivationRevisionID,
		"working_context_revision_id":    bundle.WorkingContextRevisionID,
		"recall_operator_revision_id":    bundle.RecallOperatorRevisionID,
		"consolidation_revision_id":      bundle.ConsolidationRevisionID,
		"prospective_policy_revision_id": bundle.ProspectivePolicyRevisionID}
}

func (bundle Bundle) Validate() error {
	for _, value := range bundle.revisionRecord() {
		if !identifierPattern.MatchString(value) {
			return errors.New("service policy bundle has an invalid revision identifier")
		}
	}
	encoded, err := jsoncompat.MarshalPython(bundle.revisionRecord(), true)
	if err != nil {
		return err
	}
	if bundle.RevisionID != "sha256:"+sha(string(encoded)) {
		return errors.New("service policy bundle revision does not match its references")
	}
	return nil
}

func Baseline(snapshot Snapshot) (Bundle, error) {
	catalog, err := CatalogRevision(snapshot)
	if err != nil {
		return Bundle{}, err
	}
	bundle := Bundle{CatalogRevisionID: catalog,
		ActivationRevisionID:        activationRevision,
		WorkingContextRevisionID:    workingContextRevision,
		RecallOperatorRevisionID:    recallRevision,
		ConsolidationRevisionID:     consolidationRevision,
		ProspectivePolicyRevisionID: prospectiveRevision}
	encoded, err := jsoncompat.MarshalPython(bundle.revisionRecord(), true)
	if err != nil {
		return Bundle{}, err
	}
	bundle.RevisionID = "sha256:" + sha(string(encoded))
	return bundle, bundle.Validate()
}
