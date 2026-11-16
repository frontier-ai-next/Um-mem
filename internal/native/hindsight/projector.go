package hindsight

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/jsoncompat"
	"github.com/frontier-ai-next/Um-mem/internal/store"
	"github.com/frontier-ai-next/Um-mem/memory"
)

const projectionMarker = "block_partition_v1"

type Projector struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

func (Projector) BackendID() string { return "hindsight" }

func (Projector) PartitionIdentity(record memory.MemoryRecord) (memory.NativePartitionIdentity, error) {
	id, err := partitionID(record)
	if err != nil {
		return memory.NativePartitionIdentity{}, err
	}
	membership := record.BlockMembership
	return memory.NativePartitionIdentity{BackendID: "hindsight", WorkspaceID: record.WorkspaceID,
		PrimaryBlockID: membership.PrimaryBlockID, CatalogRevisionID: membership.CatalogRevisionID,
		PartitionID: id, IsolationClass: "physical", PartitionMode: "native_bank"}, nil
}

func (p Projector) Probe(ctx context.Context) error {
	base, err := url.Parse(p.BaseURL)
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return errors.New("Hindsight base URL must be an HTTP endpoint")
	}
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet,
		strings.TrimRight(p.BaseURL, "/")+"/health", nil)
	if err != nil {
		return err
	}
	response, err := p.client().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Hindsight health endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (Projector) RequiresNative(record memory.MemoryRecord) bool { return nativeAllowed(record) }

func (p Projector) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func partitionID(record memory.MemoryRecord) (string, error) {
	if record.BlockMembership == nil || record.BlockMembership.PrimaryBlockID == "" ||
		record.BlockMembership.CatalogRevisionID == "" || record.BlockMembership.BackendPartitionID == "" ||
		record.WorkspaceID == "" {
		return "", errors.New("Hindsight native partition requires bound workspace, block, catalog revision and partition")
	}
	material, err := jsoncompat.MarshalPython([]string{record.WorkspaceID, record.BlockMembership.BackendPartitionID}, true)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(material)
	return "um-mem-bank-" + hex.EncodeToString(digest[:]), nil
}

func nativeAllowed(record memory.MemoryRecord) bool {
	if record.Status == "candidate" {
		return record.Retention == "durable" && record.DeliveryPolicy == "on_demand" &&
			record.Authority == "advisory" && record.TrustLevel == "agent_reported" &&
			record.VerificationStatus == "unverified" && len(record.VerificationMethods) == 0 &&
			record.PromotionDecision == "not_requested"
	}
	return record.Status == "accepted" && record.PromotionDecision == "accepted" &&
		len(record.VerificationMethods) > 0 && len(record.EvidenceRefs) > 0
}

func (p Projector) Project(ctx context.Context, record memory.MemoryRecord, payload string, revision int64) (store.ProjectionResult, error) {
	if record.BlockMembership == nil {
		return store.ProjectionResult{}, errors.New("Hindsight projection requires block membership")
	}
	for _, tag := range record.Tags {
		if strings.HasPrefix(tag, "um_mem:block:") {
			return store.ProjectionResult{}, errors.New("caller tags cannot use the reserved um_mem:block: prefix")
		}
	}
	if !nativeAllowed(record) {
		return store.ProjectionResult{Verified: true, Skipped: true}, nil
	}
	bankID, err := partitionID(record)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	documentID := "UM-MEM:block:" + record.BlockID
	verified, err := p.verifyDocument(ctx, bankID, documentID, record, payload, revision)
	if err != nil {
		return store.ProjectionResult{}, err
	}
	if !verified {
		bankPath := "/v1/default/banks/" + url.PathEscape(bankID)
		_, err = p.request(ctx, http.MethodPut, bankPath, map[string]any{
			"name":           "UM-MEM block " + record.BlockMembership.PrimaryBlockID,
			"retain_mission": "", "reflect_mission": "",
		})
		if err != nil {
			return store.ProjectionResult{}, err
		}
		metadata, err := nativeMetadata(record, payload, bankID, revision)
		if err != nil {
			return store.ProjectionResult{}, err
		}
		tags := append([]string{"um_mem:block:" + record.BlockMembership.PrimaryBlockID}, record.Tags...)
		contextText, timestamp := nativeEventTime(record)
		item := map[string]any{
			"content":     record.Content,
			"context":     contextText,
			"document_id": documentID,
			"metadata":    metadata,
			"tags":        tags,
		}
		if timestamp != nil {
			item["timestamp"] = timestamp
		}
		response, err := p.request(ctx, http.MethodPost, bankPath+"/memories", map[string]any{
			"items": []any{item}, "async": false,
		})
		if err != nil {
			return store.ProjectionResult{}, err
		}
		acknowledged := response["ok"] == true
		if success, present := response["success"]; present {
			acknowledged = success == true
		}
		if !acknowledged {
			return store.ProjectionResult{}, errors.New("Hindsight native write returned a negative acknowledgement")
		}
		verified, err = p.verifyDocument(ctx, bankID, documentID, record, payload, revision)
		if err != nil {
			return store.ProjectionResult{}, err
		}
		if !verified {
			return store.ProjectionResult{}, errors.New("Hindsight native document was not readable after retain")
		}
	}
	return store.ProjectionResult{NativeID: documentID, Verified: true}, nil
}

func nativeEventTime(record memory.MemoryRecord) (string, any) {
	contextText := "UM-MEM memory block: " + record.Kind
	for _, key := range []string{"occurred_at", "event_time", "timestamp", "ts", "session_date_time", "session_datetime", "session_date"} {
		raw, found := record.Metadata[key]
		if !found || raw == nil || raw == "" {
			continue
		}
		if number, ok := raw.(float64); ok {
			value := int64(number)
			return fmt.Sprintf("%s\nevent_time: %d", contextText, value), value
		}
		if number, ok := raw.(int64); ok {
			return fmt.Sprintf("%s\nevent_time: %d", contextText, number), number
		}
		if number, ok := raw.(json.Number); ok {
			if integer, err := number.Int64(); err == nil {
				return fmt.Sprintf("%s\nevent_time: %d", contextText, integer), integer
			}
			if fractional, err := number.Float64(); err == nil {
				integer := int64(fractional)
				return fmt.Sprintf("%s\nevent_time: %d", contextText, integer), integer
			}
		}
		if _, ok := raw.(bool); ok {
			return contextText + "\nevent_time_raw: " + fmt.Sprint(raw) + "\nevent_time: unset", "unset"
		}
		value := strings.TrimSpace(fmt.Sprint(raw))
		if value == "" {
			continue
		}
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02"} {
			if _, err := time.Parse(layout, value); err == nil {
				return contextText + "\nevent_time: " + value, value
			}
		}
		return contextText + "\nevent_time_raw: " + value + "\nevent_time: unset", "unset"
	}
	return contextText, nil
}

func nativeMetadata(record memory.MemoryRecord, payload, bankID string, revision int64) (map[string]string, error) {
	encode := func(value any) (string, error) {
		serialized, err := json.Marshal(value)
		return string(serialized), err
	}
	metadataJSON, err := encode(record.Metadata)
	if err != nil {
		return nil, err
	}
	tagsJSON, err := encode(record.Tags)
	if err != nil {
		return nil, err
	}
	evidenceJSON, err := encode(record.EvidenceRefs)
	if err != nil {
		return nil, err
	}
	externalJSON, err := encode(record.ExternalRefs)
	if err != nil {
		return nil, err
	}
	supersedesJSON, err := encode(record.SupersedesIDs)
	if err != nil {
		return nil, err
	}
	methodsJSON, err := encode(record.VerificationMethods)
	if err != nil {
		return nil, err
	}
	metadata := map[string]string{}
	for key, value := range record.Metadata {
		if key == "importance" || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			if typed != "" {
				metadata[key] = typed
			}
		case bool, float64, int, int64, json.Number:
			if flag, ok := value.(bool); ok {
				if flag {
					metadata[key] = "True"
				} else {
					metadata[key] = "False"
				}
			} else {
				metadata[key] = fmt.Sprint(value)
			}
		}
	}
	for key, value := range map[string]string{
		"block_id": record.BlockID, "record_id": record.BlockID, "title": record.Title,
		"kind": record.Kind, "workspace_id": record.WorkspaceID,
		"primary_block_id":  record.BlockMembership.PrimaryBlockID,
		"projection_marker": projectionMarker, "native_partition_id": bankID,
		"catalog_revision_id": record.BlockMembership.CatalogRevisionID,
		"bank_id":             bankID, "run_id": record.RunID, "subtask_id": record.SubtaskID,
		"producer": record.Producer, "scope": record.Scope, "status": record.Status,
		"retention": record.Retention, "delivery_policy": record.DeliveryPolicy,
		"temperature": record.Temperature, "authority": record.Authority,
		"trust_level": record.TrustLevel, "verification_status": record.VerificationStatus,
		"verification_methods": methodsJSON, "promotion_decision": record.PromotionDecision,
		"sensitivity": record.Sensitivity, "content_mode": record.ContentMode,
		"operational_risk": record.OperationalRisk, "revision_of": record.RevisionOf,
		"expires_at": record.ExpiresAt, "created_at": record.CreatedAt,
		"updated_at": record.UpdatedAt, "record_metadata_json": metadataJSON,
		"canonical_record_json": payload, "tags_json": tagsJSON,
		"evidence_refs_json": evidenceJSON, "external_refs_json": externalJSON,
		"supersedes_ids_json": supersedesJSON, "commit_revision": strconv.FormatInt(revision, 10),
	} {
		metadata[key] = value
	}
	for key, value := range metadata {
		if value == "" {
			delete(metadata, key)
		}
	}
	return metadata, nil
}

func (p Projector) verifyDocument(ctx context.Context, bankID, documentID string, record memory.MemoryRecord, payload string, revision int64) (bool, error) {
	path := "/v1/default/banks/" + url.PathEscape(bankID) + "/documents/" + url.PathEscape(documentID)
	document, err := p.request(ctx, http.MethodGet, path, nil)
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	metadata, ok := document["document_metadata"].(map[string]any)
	if !ok || document["id"] != documentID || document["bank_id"] != bankID ||
		document["original_text"] != record.Content || metadata["block_id"] != record.BlockID ||
		metadata["workspace_id"] != record.WorkspaceID ||
		metadata["commit_revision"] != strconv.FormatInt(revision, 10) ||
		metadata["native_partition_id"] != bankID ||
		metadata["catalog_revision_id"] != record.BlockMembership.CatalogRevisionID ||
		metadata["projection_marker"] != projectionMarker ||
		metadata["primary_block_id"] != record.BlockMembership.PrimaryBlockID {
		return false, errors.New("Hindsight native projection does not match canonical identity")
	}
	nativePayload, ok := metadata["canonical_record_json"].(string)
	if !ok || !equivalentJSON(nativePayload, payload) {
		return false, errors.New("Hindsight native canonical record differs from staged payload")
	}
	return true, nil
}

func equivalentJSON(left, right string) bool {
	var a, b any
	if json.Unmarshal([]byte(left), &a) != nil || json.Unmarshal([]byte(right), &b) != nil {
		return false
	}
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	return bytes.Equal(aJSON, bJSON)
}

var errNotFound = errors.New("Hindsight native document not found")

func (p Projector) request(ctx context.Context, method, path string, payload any) (map[string]any, error) {
	base, err := url.Parse(p.BaseURL)
	if err != nil || base == nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, errors.New("Hindsight base URL must be an HTTP endpoint")
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	endpoint := strings.TrimRight(p.BaseURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	response, err := p.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound && method == http.MethodGet {
		return nil, errNotFound
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Hindsight native endpoint returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, errors.New("Hindsight native endpoint returned a non-object response")
	}
	return object, nil
}
