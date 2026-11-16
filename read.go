package ummem

import (
	"context"
	"errors"
	"strings"

	"github.com/frontier-ai-next/Um-mem/memory"
)

// ReadExactRecord reads one canonical record by ID or exact evidence reference.
// It is an inspection operation and does not grant recall eligibility.
func (client *Client) ReadExactRecord(ctx context.Context, recordID, evidenceRef string) (*memory.MemoryRecord, error) {
	id, ref := strings.TrimSpace(recordID), strings.TrimSpace(evidenceRef)
	if (id == "") == (ref == "") {
		return nil, errors.New("read_exact_record requires exactly one record ID or evidence reference")
	}
	var record *memory.MemoryRecord
	var err error
	if id != "" {
		record, err = client.ReadStored(ctx, id)
	} else {
		record, err = client.ReadEvidence(ctx, ref)
	}
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, errors.New("exact record was not found")
	}
	return record, nil
}

// ReadExactRecords reads a bounded, distinct batch in caller order.
func (client *Client) ReadExactRecords(ctx context.Context, recordIDs []string) ([]memory.MemoryRecord, error) {
	if len(recordIDs) < 1 || len(recordIDs) > 128 {
		return nil, errors.New("read_exact_records requires 1-128 distinct record IDs")
	}
	seen := make(map[string]bool, len(recordIDs))
	result := make([]memory.MemoryRecord, 0, len(recordIDs))
	for _, id := range recordIDs {
		if id == "" || id != strings.TrimSpace(id) || seen[id] {
			return nil, errors.New("read_exact_records requires 1-128 distinct record IDs")
		}
		seen[id] = true
		record, err := client.ReadExactRecord(ctx, id, "")
		if err != nil {
			return nil, err
		}
		result = append(result, *record)
	}
	return result, nil
}

// InspectRecord requires the exact record, logical block, and revision IDs.
func (client *Client) InspectRecord(ctx context.Context, recordID, logicalBlockID,
	expectedRevisionID string) (*memory.MemoryRecord, error) {
	if recordID == "" || logicalBlockID == "" || expectedRevisionID == "" ||
		recordID != strings.TrimSpace(recordID) || logicalBlockID != strings.TrimSpace(logicalBlockID) ||
		expectedRevisionID != strings.TrimSpace(expectedRevisionID) {
		return nil, errors.New("record inspection requires exact record, block and revision IDs")
	}
	record, err := client.ReadExactRecord(ctx, recordID, "")
	if err != nil {
		return nil, err
	}
	if record.RevisionID != expectedRevisionID || record.BlockMembership == nil ||
		record.BlockMembership.PrimaryBlockID != logicalBlockID {
		return nil, errors.New("record is unavailable at the requested block and revision")
	}
	return record, nil
}
