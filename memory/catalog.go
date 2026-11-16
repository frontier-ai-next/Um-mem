package memory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
)

type BlockGuidanceCard struct {
	BlockID   string   `json:"block_id"`
	Synopsis  string   `json:"synopsis"`
	UseWhen   []string `json:"use_when"`
	AvoidWhen []string `json:"avoid_when"`
}

type BlockDescriptor struct {
	BlockID              string            `json:"block_id"`
	BlockType            string            `json:"block_type"`
	ParentID             string            `json:"parent_id"`
	Scope                string            `json:"scope"`
	Authority            string            `json:"authority"`
	DeliveryPolicy       string            `json:"delivery_policy"`
	Retention            string            `json:"retention"`
	Producer             string            `json:"producer"`
	Consumers            []string          `json:"consumers"`
	RecallPath           string            `json:"recall_path"`
	LifecyclePolicy      map[string]any    `json:"lifecycle_policy"`
	IsolationPolicy      map[string]any    `json:"isolation_policy"`
	TokenBudget          int               `json:"token_budget"`
	SemanticMemberships  []string          `json:"semantic_memberships"`
	Guidance             BlockGuidanceCard `json:"guidance"`
	DescriptorRevisionID string            `json:"descriptor_revision_id"`
}

type SemanticCatalog struct {
	Descriptors     []BlockDescriptor `json:"descriptors"`
	DynamicLabels   []any             `json:"dynamic_labels"`
	RefinementCases []any             `json:"refinement_cases"`
	RevisionID      string            `json:"revision_id"`
}

func revisionID(prefix string, payload any) (string, error) {
	encoded, err := jsoncompat.MarshalPython(payload, false)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return prefix + ":sha256:" + hex.EncodeToString(hash[:]), nil
}

func guidanceTexts(value any) []string {
	var source []any
	switch typed := value.(type) {
	case string:
		source = []any{typed}
	case []string:
		for _, item := range typed {
			source = append(source, item)
		}
	case []any:
		source = typed
	}
	result := make([]string, 0, len(source))
	seen := map[string]bool{}
	for _, item := range source {
		text := strings.TrimSpace(fmt.Sprint(item))
		if text != "" && !seen[text] {
			result = append(result, text)
			seen[text] = true
		}
	}
	return result
}

func descriptorFor(block Block) (BlockDescriptor, error) {
	if err := block.Validate(); err != nil {
		return BlockDescriptor{}, err
	}
	memberships := uniqueTexts(block.SemanticMemberships)
	if len(memberships) == 0 {
		memberships = uniqueTexts(append([]string{block.BlockType}, block.Tags...))
	}
	synopsis := strings.TrimSpace(fmt.Sprint(block.Guidance["synopsis"]))
	if block.Guidance["synopsis"] == nil || synopsis == "" {
		synopsis = strings.ReplaceAll(block.BlockID, "_", " ") + " (" + block.BlockType + ")"
	}
	useWhen := guidanceTexts(block.Guidance["use_when"])
	if len(useWhen) == 0 {
		useWhen = uniqueTexts(append(slices.Clone(block.SemanticMemberships), block.Tags...))
	}
	producer := block.Producer
	if producer == "" {
		producer = block.Owner
	}
	consumers := slices.Clone(block.Consumers)
	if len(consumers) == 0 {
		consumers = []string{"memory_agent"}
	}
	recallPath := block.RecallPath
	if recallPath == "" {
		recallPath = "hybrid_recall"
	}
	descriptor := BlockDescriptor{
		BlockID: block.BlockID, BlockType: block.BlockType, ParentID: block.ParentID,
		Scope: block.Scope, Authority: block.Authority, DeliveryPolicy: block.DeliveryPolicy,
		Retention: block.Retention, Producer: producer, Consumers: consumers,
		RecallPath: recallPath, LifecyclePolicy: block.LifecyclePolicy,
		IsolationPolicy: block.IsolationPolicy, TokenBudget: block.TokenBudget,
		SemanticMemberships: memberships,
		Guidance: BlockGuidanceCard{BlockID: block.BlockID, Synopsis: synopsis,
			UseWhen: useWhen, AvoidWhen: guidanceTexts(block.Guidance["avoid_when"])},
	}
	if descriptor.Guidance.UseWhen == nil {
		descriptor.Guidance.UseWhen = []string{}
	}
	if descriptor.Guidance.AvoidWhen == nil {
		descriptor.Guidance.AvoidWhen = []string{}
	}
	encoded, err := jsoncompat.MarshalPython(descriptor, false)
	if err != nil {
		return BlockDescriptor{}, err
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return BlockDescriptor{}, err
	}
	delete(payload, "descriptor_revision_id")
	descriptor.DescriptorRevisionID, err = revisionID("descriptor", payload)
	return descriptor, err
}

// CompileSemanticCatalog preserves Python's descriptor and catalog revision
// hashes so new Go records can coexist with existing v9 memberships.
func CompileSemanticCatalog(blocks []Block) (SemanticCatalog, error) {
	if len(blocks) == 0 {
		return SemanticCatalog{}, errors.New("semantic catalog requires descriptors")
	}
	descriptors := make([]BlockDescriptor, 0, len(blocks))
	seen := map[string]bool{}
	for _, block := range blocks {
		if seen[block.BlockID] {
			return SemanticCatalog{}, fmt.Errorf("duplicate semantic catalog block %q", block.BlockID)
		}
		seen[block.BlockID] = true
		descriptor, err := descriptorFor(block)
		if err != nil {
			return SemanticCatalog{}, err
		}
		descriptors = append(descriptors, descriptor)
	}
	slices.SortFunc(descriptors, func(a, b BlockDescriptor) int { return strings.Compare(a.BlockID, b.BlockID) })
	catalog := SemanticCatalog{Descriptors: descriptors, DynamicLabels: []any{}, RefinementCases: []any{}}
	var err error
	catalog.RevisionID, err = revisionID("catalog", map[string]any{
		"descriptors": catalog.Descriptors, "dynamic_labels": catalog.DynamicLabels,
		"refinement_cases": catalog.RefinementCases,
	})
	return catalog, err
}
