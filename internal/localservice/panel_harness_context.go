package localservice

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf16"
)

var harnessTraceName = regexp.MustCompile(`^\d{17}-[0-9a-f]{16}\.json$`)

type harnessTraceRecord struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Chars    int    `json:"chars,omitempty"`
	Clipped  bool   `json:"clipped,omitempty"`
	Text     string `json:"text,omitempty"`
}

type harnessSearch struct {
	Operation string               `json:"operation"`
	Chars     int                  `json:"chars"`
	Skipped   string               `json:"skipped,omitempty"`
	Records   []harnessTraceRecord `json:"records"`
}

type harnessTurn struct {
	ID        string               `json:"id"`
	Session   string               `json:"session"`
	Turn      string               `json:"turn"`
	At        string               `json:"at"`
	Status    string               `json:"status"`
	Chars     int                  `json:"chars"`
	Automatic []harnessTraceRecord `json:"automatic"`
	Searches  []harnessSearch      `json:"searches"`
}

func harnessContext(identity Identity) []harnessTurn {
	trace := make([]harnessTurn, 0, 40)
	scope := sha256.Sum256([]byte(identity.WorkspaceID + "\x00" + identity.ProfileID))
	root := filepath.Join(identity.RepoRoot, ".UM-MEM", "memory", "harness-context", hex.EncodeToString(scope[:]))
	entries, err := os.ReadDir(root)
	if err != nil {
		return trace
	}
	for index := len(entries) - 1; index >= 0 && len(trace) < cap(trace); index-- {
		entry := entries[index]
		if !entry.Type().IsRegular() || !harnessTraceName.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Size() > 64*1024 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		var turn harnessTurn
		if err != nil || json.Unmarshal(data, &turn) != nil || turn.ID == "" {
			continue
		}
		// A previous preview build included text. Deliver only a canonical
		// lookup handle and length, so the panel observes erasure and scope.
		clean := func(records []harnessTraceRecord) {
			for i := range records {
				record := &records[i]
				if record.Text != "" {
					record.Chars = len(utf16.Encode([]rune(record.Text)))
					record.Clipped = strings.HasSuffix(record.Text, "…")
					record.Text = ""
				}
			}
		}
		clean(turn.Automatic)
		for _, search := range turn.Searches {
			clean(search.Records)
		}
		trace = append(trace, turn)
	}
	return trace
}
