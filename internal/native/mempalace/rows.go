package mempalace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/jsoncompat"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
	"github.com/gomlx/go-huggingface/tokenizers/api"
	"github.com/gomlx/go-huggingface/tokenizers/hftokenizer"
)

const projectionMarker = "mempalace_collection_family_v1"

func openTokenizer(modelDir string) (*hftokenizer.Tokenizer, error) {
	data, err := os.ReadFile(filepath.Join(modelDir, "tokenizer.json"))
	if err != nil {
		return nil, err
	}
	tokenizer, err := hftokenizer.NewFromContent(nil, data)
	if err != nil {
		return nil, err
	}
	if err := tokenizer.With(api.EncodeOptions{AddSpecialTokens: true, IncludeSpans: true}); err != nil {
		return nil, err
	}
	return tokenizer, nil
}

func jsonString(value any) (string, error) {
	encoded, err := jsoncompat.MarshalPython(value, false)
	return string(encoded), err
}

func metadataString(record memory.MemoryRecord, key string) string {
	value, exists := record.Metadata[key]
	if !exists || value == nil || value == "" {
		return ""
	}
	return fmt.Sprint(value)
}

func nativeMetadata(record memory.MemoryRecord, revision int64) (map[string]any, error) {
	partition, err := Partition(record)
	if err != nil {
		return nil, err
	}
	if revision < 1 {
		return nil, errors.New("MemPalace native row requires positive commit revision")
	}
	canonical, err := jsonString(record)
	if err != nil {
		return nil, err
	}
	evidence, err := jsonString(record.EvidenceRefs)
	if err != nil {
		return nil, err
	}
	external, err := jsonString(record.ExternalRefs)
	if err != nil {
		return nil, err
	}
	metadataJSON, err := jsonString(record.Metadata)
	if err != nil {
		return nil, err
	}
	sourcePath := metadataString(record, "source_path")
	if sourcePath == "" {
		sourcePath = metadataString(record, "evidence_id")
	}
	if sourcePath == "" {
		sourcePath = "memory_record"
	}
	sourceBackend := record.SourceBackend
	if sourceBackend == "" {
		sourceBackend = "mempalace"
	}
	tier := record.Temperature
	if tier == "" {
		tier = "warm"
	}
	verified := 0
	if record.VerificationStatus == "verified" {
		verified = 1
	}
	return map[string]any{
		"id": record.BlockID, "store": StoreForBlock(record), "tier": tier,
		"scope": record.Scope, "tags": strings.Join(record.Tags, ","),
		"stage_id": "", "subtask_id": record.SubtaskID, "run_id": record.RunID,
		"source_path": sourcePath, "verified": verified,
		"workspace_id": record.WorkspaceID, "logical_block_id": partition.PrimaryBlockID,
		"projection_marker": projectionMarker, "native_partition_id": partition.PartitionID,
		"catalog_revision_id": partition.CatalogRevisionID,
		"kind":                record.Kind, "title": record.Title, "status": record.Status,
		"retention": record.Retention, "delivery_policy": record.DeliveryPolicy,
		"temperature": record.Temperature, "authority": record.Authority,
		"trust_level": record.TrustLevel, "verification_status": record.VerificationStatus,
		"verification_methods": strings.Join(record.VerificationMethods, ","),
		"promotion_decision":   record.PromotionDecision, "source_backend": sourceBackend,
		"storage_backend": "mempalace", "evidence_refs_json": evidence,
		"external_refs_json": external, "sensitivity": record.Sensitivity,
		"content_mode": record.ContentMode, "operational_risk": record.OperationalRisk,
		"metadata_json": metadataJSON, "canonical_record_json": canonical,
		"evidence_id": metadataString(record, "evidence_id"),
		"producer":    record.Producer, "created_at": record.CreatedAt,
		"updated_at": record.UpdatedAt, "expires_at": record.ExpiresAt,
		"commit_revision": revision,
	}, nil
}

// NativeRows preserves the complete canonical document and, when the MiniLM
// token limit is exceeded, adds the same overlapping derived windows as Python.
// The tokenizer spans are UTF-8 byte offsets, matching the persisted metadata.
func NativeRows(record memory.MemoryRecord, revision int64, tokenizer *hftokenizer.Tokenizer) ([]NativeRow, error) {
	if tokenizer == nil {
		return nil, errors.New("MemPalace tokenizer is unavailable")
	}
	metadata, err := nativeMetadata(record, revision)
	if err != nil {
		return nil, err
	}
	rows := []NativeRow{{ID: record.BlockID, Document: record.Content, Metadata: metadata}}
	encoded := tokenizer.EncodeWithAnnotations(record.Content)
	spans := make([]api.TokenSpan, 0, len(encoded.Spans))
	for _, span := range encoded.Spans {
		if span.End > span.Start {
			spans = append(spans, span)
		}
	}
	if len(spans) <= 254 {
		return rows, nil
	}
	partition, err := Partition(record)
	if err != nil {
		return nil, err
	}
	for start, ordinal := 0, 0; start < len(spans); start, ordinal = start+160, ordinal+1 {
		end := min(len(spans), start+200)
		startByte := 0
		if start > 0 {
			startByte = spans[start].Start
		}
		endByte := len(record.Content)
		if end < len(spans) {
			endByte = spans[end-1].End
		}
		if startByte < 0 || endByte > len(record.Content) || startByte >= endByte {
			return nil, errors.New("MemPalace tokenizer returned invalid window offsets")
		}
		rows = append(rows, NativeRow{
			ID:       fmt.Sprintf("%s:window:%04d", record.BlockID, ordinal),
			Document: record.Content[startByte:endByte],
			Metadata: map[string]any{
				"derived_window": 1, "canonical_record_id": record.BlockID,
				"window_start_byte": startByte, "window_end_byte": endByte,
				"workspace_id": record.WorkspaceID, "logical_block_id": partition.PrimaryBlockID,
				"native_partition_id": partition.PartitionID,
				"catalog_revision_id": partition.CatalogRevisionID,
				"projection_marker":   projectionMarker, "commit_revision": revision,
			},
		})
		if end == len(spans) {
			break
		}
	}
	return rows, nil
}

func nativeRecordJSON(metadata map[string]any) (memory.MemoryRecord, error) {
	raw, ok := metadata["canonical_record_json"].(string)
	if !ok || raw == "" {
		return memory.MemoryRecord{}, errors.New("MemPalace native canonical record is missing")
	}
	return memory.DecodeCanonicalRecord([]byte(raw))
}

func sameNativeMetadata(actual, expected map[string]any) bool {
	for key, expectedValue := range expected {
		actualValue, ok := actual[key]
		if !ok {
			return false
		}
		if strings.HasSuffix(key, "_json") {
			actualJSON, actualOK := actualValue.(string)
			expectedJSON, expectedOK := expectedValue.(string)
			if !actualOK || !expectedOK || !sameJSONValue(actualJSON, expectedJSON) {
				return false
			}
			continue
		}
		left, _ := json.Marshal(actualValue)
		right, _ := json.Marshal(expectedValue)
		if string(left) != string(right) {
			return false
		}
	}
	return true
}

func sameJSONValue(left, right string) bool {
	var a, b any
	leftDecoder := json.NewDecoder(bytes.NewReader([]byte(left)))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader([]byte(right)))
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&a) != nil || rightDecoder.Decode(&b) != nil {
		return false
	}
	var trailing any
	if leftDecoder.Decode(&trailing) != io.EOF || rightDecoder.Decode(&trailing) != io.EOF {
		return false
	}
	encodedA, _ := json.Marshal(a)
	encodedB, _ := json.Marshal(b)
	return string(encodedA) == string(encodedB)
}
