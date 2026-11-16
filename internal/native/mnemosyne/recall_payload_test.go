package mnemosyne

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidatedCanonicalPayloadPreservesNativeVerification(t *testing.T) {
	record, _ := boundFixture(t)
	record.Metadata["precise_counter"] = json.Number("9007199254740993")
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	canonical := string(payload)
	partition, err := PartitionID(record)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := projectionMetadata(record, canonical, 7, partition)
	if err != nil {
		t.Fatal(err)
	}
	base := nativeRow{ID: "native-1", Source: source(record), Session: partition,
		Content: contentEnvelope(record, 7), Metadata: metadata}
	setMetadata := func(row nativeRow, key string, value any) nativeRow {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(row.Metadata), &fields); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		fields[key] = encoded
		encoded, err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		row.Metadata = string(encoded)
		return row
	}
	foreign := base
	foreign.Session = "foreign-partition"
	forged := base
	forged.Content = "forged content"
	for _, test := range []struct {
		name string
		row  nativeRow
		ok   bool
	}{
		{"identical payload", base, true},
		{"equivalent JSON encoding", setMetadata(base, "canonical_record_json", canonical+"\n"), true},
		{"different large integer", setMetadata(base, "canonical_record_json",
			strings.ReplaceAll(canonical, "9007199254740993", "9007199254740992")), false},
		{"invalid canonical JSON", setMetadata(base, "canonical_record_json", "{}"), false},
		{"missing canonical JSON", setMetadata(base, "canonical_record_json", nil), false},
		{"wrong journal revision", setMetadata(base, "commit_revision", 8), false},
		{"wrong content digest", setMetadata(base, "canonical_content_sha256", "forged"), false},
		{"foreign partition", foreign, false},
		{"forged content", forged, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fullErr := verifyNative(test.row, record, 7, partition)
			fastErr := verifyNativePayload(test.row, record, 7, partition, canonical)
			if (fullErr == nil) != test.ok || (fastErr == nil) != test.ok {
				t.Fatalf("verification differs: full=%v fast=%v expectedOK=%v", fullErr, fastErr, test.ok)
			}
		})
	}
}
