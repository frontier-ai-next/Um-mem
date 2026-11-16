package memory

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var maskUUID = regexp.MustCompile(`(?i)^[0-9a-f]{32}$`)

func validMaskUUID(value string) bool {
	value = strings.TrimPrefix(strings.ToLower(value), "urn:uuid:")
	value = strings.Trim(value, "{}")
	return maskUUID.MatchString(strings.ReplaceAll(value, "-", ""))
}

type ActiveMemorySelector struct {
	BlockID        string   `json:"block_id"`
	SelectorType   string   `json:"selector_type"`
	RecordIDs      []string `json:"record_ids"`
	Query          string   `json:"query"`
	Limit          int      `json:"limit"`
	WorkingStateID string   `json:"working_state_id"`
}

type ActiveMemoryPriority struct {
	BlockID  string `json:"block_id"`
	Priority int    `json:"priority"`
}

type ActiveMemoryBlockBudget struct {
	BlockID     string `json:"block_id"`
	TokenBudget int    `json:"token_budget"`
}

// ActiveMemoryMask is the durable, scope-specific selection of catalog blocks.
// Its JSON field names and empty-array representation match the Python contract.
type ActiveMemoryMask struct {
	WorkspaceID       string                    `json:"workspace_id"`
	ProfileID         string                    `json:"profile_id"`
	Revision          int                       `json:"revision"`
	Status            string                    `json:"status"`
	SelectedBlockIDs  []string                  `json:"selected_block_ids"`
	TokenBudget       int                       `json:"token_budget"`
	ActivationReason  string                    `json:"activation_reason"`
	SessionID         string                    `json:"session_id"`
	TaskID            string                    `json:"task_id"`
	ContextRefs       []string                  `json:"context_refs"`
	PinnedRecordIDs   []string                  `json:"pinned_record_ids"`
	Selectors         []ActiveMemorySelector    `json:"selectors"`
	Priorities        []ActiveMemoryPriority    `json:"priorities"`
	BlockTokenBudgets []ActiveMemoryBlockBudget `json:"block_token_budgets"`
	Lifecycle         string                    `json:"lifecycle"`
	CreatedAt         string                    `json:"created_at"`
	UpdatedAt         string                    `json:"updated_at"`
	ExpiresAt         string                    `json:"expires_at"`
	CatalogRevision   int                       `json:"catalog_revision"`
	MaskID            string                    `json:"mask_id"`
}

func maskTexts(values []string, field string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("%s requires nonempty values", field)
		}
		if !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
	}
	return result, nil
}

func maskTime(value, field string) error {
	if value == "" {
		return nil
	}
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return fmt.Errorf("%s must include a timezone: %w", field, err)
	}
	return nil
}

func (mask *ActiveMemoryMask) Normalize() error {
	mask.WorkspaceID = strings.TrimSpace(mask.WorkspaceID)
	mask.ProfileID = strings.TrimSpace(mask.ProfileID)
	mask.MaskID = strings.TrimSpace(mask.MaskID)
	mask.SessionID = strings.TrimSpace(mask.SessionID)
	mask.TaskID = strings.TrimSpace(mask.TaskID)
	mask.ActivationReason = strings.TrimSpace(mask.ActivationReason)
	mask.Lifecycle = strings.TrimSpace(mask.Lifecycle)
	mask.CreatedAt = strings.TrimSpace(mask.CreatedAt)
	mask.UpdatedAt = strings.TrimSpace(mask.UpdatedAt)
	mask.ExpiresAt = strings.TrimSpace(mask.ExpiresAt)
	var err error
	for _, item := range []struct {
		field string
		list  *[]string
	}{
		{"selected_block_ids", &mask.SelectedBlockIDs},
		{"context_refs", &mask.ContextRefs},
		{"pinned_record_ids", &mask.PinnedRecordIDs},
	} {
		*item.list, err = maskTexts(*item.list, item.field)
		if err != nil {
			return err
		}
	}
	if mask.Selectors == nil {
		mask.Selectors = []ActiveMemorySelector{}
	}
	if mask.Priorities == nil {
		mask.Priorities = []ActiveMemoryPriority{}
	}
	if mask.BlockTokenBudgets == nil {
		mask.BlockTokenBudgets = []ActiveMemoryBlockBudget{}
	}
	for i := range mask.Selectors {
		selector := &mask.Selectors[i]
		selector.BlockID = strings.TrimSpace(selector.BlockID)
		selector.Query = strings.TrimSpace(selector.Query)
		selector.WorkingStateID = strings.TrimSpace(selector.WorkingStateID)
		selector.RecordIDs, err = maskTexts(selector.RecordIDs, "record_ids")
		if err != nil {
			return err
		}
	}
	for i := range mask.Priorities {
		mask.Priorities[i].BlockID = strings.TrimSpace(mask.Priorities[i].BlockID)
	}
	for i := range mask.BlockTokenBudgets {
		mask.BlockTokenBudgets[i].BlockID = strings.TrimSpace(mask.BlockTokenBudgets[i].BlockID)
	}
	return mask.Validate()
}

func (mask ActiveMemoryMask) Validate() error {
	if mask.WorkspaceID == "" || mask.ProfileID == "" || mask.ActivationReason == "" || mask.Lifecycle == "" {
		return errors.New("active memory mask requires workspace, profile, reason and lifecycle")
	}
	if !validMaskUUID(mask.MaskID) {
		return errors.New("mask_id must be a UUID")
	}
	if mask.Revision < 1 || mask.TokenBudget < 0 || mask.CatalogRevision < 0 {
		return errors.New("active memory mask has invalid revision or budget")
	}
	if !oneOf(mask.Status, "active", "suspended", "expired") {
		return errors.New("active memory mask status is unsupported")
	}
	for _, value := range []struct{ name, time string }{
		{"created_at", mask.CreatedAt}, {"updated_at", mask.UpdatedAt}, {"expires_at", mask.ExpiresAt},
	} {
		if err := maskTime(value.time, value.name); err != nil {
			return err
		}
	}
	selected := make(map[string]bool, len(mask.SelectedBlockIDs))
	for _, id := range mask.SelectedBlockIDs {
		selected[id] = true
	}
	for _, selector := range mask.Selectors {
		if selector.BlockID == "" || !selected[selector.BlockID] {
			return errors.New("mask selector must reference a selected block")
		}
		if !oneOf(selector.SelectorType, "pinned_records", "canonical_heads", "latest_records", "backend_query", "working_state") {
			return errors.New("selector_type is unsupported")
		}
		if selector.Limit < 0 || selector.SelectorType == "pinned_records" && len(selector.RecordIDs) == 0 ||
			selector.SelectorType == "backend_query" && selector.Query == "" ||
			selector.SelectorType != "backend_query" && selector.Query != "" ||
			selector.SelectorType != "working_state" && selector.WorkingStateID != "" {
			return errors.New("active memory selector fields are invalid")
		}
	}
	for _, priority := range mask.Priorities {
		if priority.BlockID == "" || !selected[priority.BlockID] {
			return errors.New("mask priority must reference a selected block")
		}
	}
	for _, budget := range mask.BlockTokenBudgets {
		if budget.BlockID == "" || !selected[budget.BlockID] || budget.TokenBudget < 0 {
			return errors.New("mask block budget is invalid")
		}
	}
	return nil
}

func (mask ActiveMemoryMask) IsExpired(at time.Time) bool {
	if mask.Status == "expired" {
		return true
	}
	if mask.ExpiresAt == "" {
		return false
	}
	expiry, err := time.Parse(time.RFC3339Nano, mask.ExpiresAt)
	return err != nil || !expiry.After(at)
}
