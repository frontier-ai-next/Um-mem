package jsoncompat

import "testing"

func TestMarshalPythonSortsKeysAndPreservesEncodingModes(t *testing.T) {
	value := map[string]any{
		"z": "é&<",
		"a": map[string]any{"u": "\u2028", "literal": `\u2028`},
	}
	unicode, err := MarshalPython(value, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(unicode), "{\"a\":{\"literal\":\"\\\\u2028\",\"u\":\"\u2028\"},\"z\":\"é&<\"}"; got != want {
		t.Fatalf("ensure_ascii=False differs: %q", got)
	}
	ascii, err := MarshalPython(value, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(ascii), `{"a":{"literal":"\\u2028","u":"\u2028"},"z":"\u00e9&<"}`; got != want {
		t.Fatalf("ensure_ascii=True differs: %q", got)
	}
}

func TestMarshalPythonDefaultMatchesIndexNodeEncoding(t *testing.T) {
	value := map[string]any{"z": []any{"a,b:c", "é"},
		"a": map[string]any{"quoted": "say \"hello\""}}
	encoded, err := MarshalPythonDefault(value, false)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a": {"quoted": "say \"hello\""}, "z": ["a,b:c", "é"]}`
	if string(encoded) != want {
		t.Fatalf("Python default JSON separators differ: %q", encoded)
	}
}
