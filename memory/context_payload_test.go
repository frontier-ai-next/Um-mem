package memory

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestPythonContextPackerParity(t *testing.T) {
	fixture, err := os.ReadFile("testdata/context_payload_python.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name         string                `json:"name"`
		Items        []map[string]any      `json:"items"`
		MaxTokens    int                   `json:"max_tokens"`
		Requirements []EvidenceRequirement `json:"requirements"`
		Expected     json.RawMessage       `json:"expected"`
	}
	if err := json.Unmarshal(fixture, &cases); err != nil {
		t.Fatal(err)
	}
	for _, testcase := range cases {
		t.Run(testcase.Name, func(t *testing.T) {
			actual, err := PackEvidencePayload(testcase.Items, PackOptions{
				MaxTokens: testcase.MaxTokens, Requirements: testcase.Requirements})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(actual)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(testcase.Expected, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Go context payload differs from Python fixture:\nGo: %s\nPython: %s", encoded, testcase.Expected)
			}
		})
	}
}

func TestPythonContextAssuranceParity(t *testing.T) {
	fixture, err := os.ReadFile("testdata/context_assurance_python.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string          `json:"name"`
		Metadata map[string]any  `json:"metadata"`
		Expected json.RawMessage `json:"expected"`
	}
	if err := json.Unmarshal(fixture, &cases); err != nil {
		t.Fatal(err)
	}
	for _, testcase := range cases {
		t.Run(testcase.Name, func(t *testing.T) {
			actual := compactContextMetadata(testcase.Metadata)
			encoded, err := json.Marshal(actual)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(testcase.Expected, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Go context assurance differs from Python fixture:\nGo: %s\nPython: %s", encoded, testcase.Expected)
			}
		})
	}
}
