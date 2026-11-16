package jsoncompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"
)

// MarshalPython emits compact, recursively key-sorted JSON with Python's
// ensure_ascii setting. Persisted hashes and native bank IDs depend on these
// exact bytes, including the distinction between escaped and literal Unicode.
func MarshalPython(value any, ensureASCII bool) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(buffer.Bytes()))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return nil, err
	}
	buffer.Reset()
	encoder = json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(normalized); err != nil {
		return nil, err
	}
	compact := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	if !ensureASCII {
		var literal bytes.Buffer
		for offset := 0; offset < len(compact); {
			if compact[offset] == '\\' && offset+5 < len(compact) && compact[offset+1] == 'u' {
				switch strings.ToLower(string(compact[offset+2 : offset+6])) {
				case "2028":
					literal.WriteRune('\u2028')
					offset += 6
					continue
				case "2029":
					literal.WriteRune('\u2029')
					offset += 6
					continue
				}
			}
			if compact[offset] == '\\' && offset+1 < len(compact) {
				literal.Write(compact[offset : offset+2])
				offset += 2
				continue
			}
			literal.WriteByte(compact[offset])
			offset++
		}
		return literal.Bytes(), nil
	}
	var ascii bytes.Buffer
	for _, char := range string(compact) {
		if char < 128 {
			ascii.WriteByte(byte(char))
			continue
		}
		for _, unit := range utf16.Encode([]rune{char}) {
			fmt.Fprintf(&ascii, "\\u%04x", unit)
		}
	}
	return ascii.Bytes(), nil
}

// MarshalPythonDefault uses json.dumps' default separators while preserving
// sorted keys and the requested ensure_ascii behavior. Persisted Python index
// nodes use this form rather than compact canonical-record JSON.
func MarshalPythonDefault(value any, ensureASCII bool) ([]byte, error) {
	compact, err := MarshalPython(value, ensureASCII)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	inString, escaped := false, false
	for _, char := range compact {
		if inString {
			output.WriteByte(char)
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == '"' {
				inString = false
			}
			continue
		}
		output.WriteByte(char)
		if char == '"' {
			inString = true
		} else if char == ',' || char == ':' {
			output.WriteByte(' ')
		}
	}
	return output.Bytes(), nil
}
