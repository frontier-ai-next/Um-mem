package ledger

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var skippedNames = map[string]bool{
	".git": true, ".hg": true, ".svn": true, ".memory": true,
	".UM-MEM": true, ".UM_MEM_scratch": true, ".venv": true,
	"venv": true, "__pycache__": true, "node_modules": true,
	"dist": true, "build": true, "coverage": true,
	".pytest_cache": true, ".mypy_cache": true, ".ruff_cache": true, ".cache": true,
}

func fileHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.CopyBuffer(hasher, file, make([]byte, 1024*1024)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// WorkspaceHash matches the Python contract's sorted (relative path, SHA-256)
// rows while excluding generated memory, dependency and VCS directories.
func WorkspaceHash(root string) (string, error) {
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	paths := make([]string, 0)
	if err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != base && skippedNames[entry.Name()] {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			info, err := os.Stat(path)
			if err == nil && info.Mode().IsRegular() {
				paths = append(paths, path)
			}
		}
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(paths)
	rows := make([][]string, 0, len(paths))
	for _, path := range paths {
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return "", err
		}
		hash, err := fileHash(path)
		if err != nil {
			return "", err
		}
		rows = append(rows, []string{filepath.ToSlash(rel), hash})
	}
	return HashValue(rows)
}

// DiffHash binds a caller's changed-file list to exact current bytes. Missing
// paths are represented explicitly and paths escaping the root are ignored.
func DiffHash(root string, changedFiles []string) (string, error) {
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	rows := make([][]string, 0, len(changedFiles))
	for _, raw := range changedFiles {
		rel := strings.TrimLeft(strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/")), "./")
		if rel == "" {
			continue
		}
		skip := false
		for _, part := range strings.Split(rel, "/") {
			if skippedNames[part] {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		candidate := filepath.Join(base, filepath.FromSlash(rel))
		resolved, err := resolveNonStrict(candidate, 0)
		if err != nil {
			return "", err
		}
		inside, err := filepath.Rel(base, resolved)
		if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
			continue
		}
		hash := "<missing>"
		if info, err := os.Stat(resolved); err == nil && info.Mode().IsRegular() {
			hash, err = fileHash(resolved)
			if err != nil {
				return "", err
			}
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		rows = append(rows, []string{rel, hash})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i][0] != rows[j][0] {
			return rows[i][0] < rows[j][0]
		}
		return rows[i][1] < rows[j][1]
	})
	return HashValue(rows)
}

// Python Path.resolve() follows an existing symlink prefix even when the
// final file is missing. EvalSymlinks alone reports ENOENT for that case.
func resolveNonStrict(path string, depth int) (string, error) {
	if depth > 64 {
		return "", errors.New("changed file path has too many symlink components")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if target, readErr := os.Readlink(path); readErr == nil {
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		return resolveNonStrict(target, depth+1)
	}
	parent := filepath.Dir(path)
	if parent == path {
		return filepath.Clean(path), nil
	}
	resolvedParent, err := resolveNonStrict(parent, depth+1)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(path)), nil
}

type VerificationReport struct {
	ReportID             string `json:"report_id"`
	ReportHash           string `json:"report_hash"`
	WorkspaceHash        string `json:"workspace_hash"`
	DiffHash             string `json:"diff_hash"`
	ProducedAfterEventID string `json:"produced_after_event_id"`
	VerifierID           string `json:"verifier_id"`
	Passed               bool   `json:"passed"`
	LedgerHash           string `json:"ledger_hash"`
}

// FindVerificationReport reuses a previously committed report after an
// interrupted operation receipt. Its event hash is checked before reuse.
func FindVerificationReport(repoRoot, workspaceID, stageID, verifierID,
	reportHash string, passed bool) (*VerificationReport, error) {
	path, err := Path(repoRoot, workspaceID)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	for scanner.Scan() {
		var row map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			continue
		}
		if row["stage_id"] != stageID || row["tool"] != "verification_report" || row["actor"] != "verifier" {
			continue
		}
		result, ok := row["result"].(map[string]any)
		if !ok || result["verifier_id"] != verifierID || result["report_hash"] != reportHash ||
			result["passed"] != passed {
			return nil, errors.New("host verification report conflicts with prior event")
		}
		eventHash, ok := row["event_hash"].(string)
		if !ok {
			return nil, errors.New("prior host verification event has no hash")
		}
		delete(row, "event_hash")
		computed, err := HashValue(row)
		if err != nil || computed != eventHash {
			return nil, errors.New("prior host verification event hash is invalid")
		}
		id, _ := row["event_id"].(string)
		workspaceHash, _ := result["workspace_hash"].(string)
		diffHash, _ := result["diff_hash"].(string)
		after, _ := result["produced_after_event_id"].(string)
		typedHash, err := HashValue(map[string]any{"report_hash": reportHash,
			"passed": passed, "workspace_hash": workspaceHash, "diff_hash": diffHash})
		if err != nil || row["result_hash"] != typedHash || id == "" ||
			workspaceHash == "" || diffHash == "" {
			return nil, errors.New("prior host verification result hash is invalid")
		}
		return &VerificationReport{ReportID: id, ReportHash: reportHash,
			WorkspaceHash: workspaceHash, DiffHash: diffHash,
			ProducedAfterEventID: after, VerifierID: verifierID,
			Passed: passed, LedgerHash: eventHash}, nil
	}
	return nil, scanner.Err()
}

func AppendVerificationReport(repoRoot, workspaceID, verifierID, reportHash, stageID,
	workspaceRoot string, changedFiles []string, passed bool) (VerificationReport, error) {
	workspaceHash, err := WorkspaceHash(workspaceRoot)
	if err != nil {
		return VerificationReport{}, err
	}
	diffHash, err := DiffHash(workspaceRoot, changedFiles)
	if err != nil {
		return VerificationReport{}, err
	}
	resultHash, err := HashValue(map[string]any{"report_hash": reportHash, "passed": passed,
		"workspace_hash": workspaceHash, "diff_hash": diffHash})
	if err != nil {
		return VerificationReport{}, err
	}
	result := map[string]any{"verifier_id": verifierID, "report_hash": reportHash,
		"passed": passed, "workspace_hash": workspaceHash, "diff_hash": diffHash,
		"produced_after_event_id": ""}
	event, err := AppendWithResultHash(repoRoot, workspaceID, "verifier", stageID,
		"verification_report", result, resultHash)
	if err != nil {
		return VerificationReport{}, err
	}
	return VerificationReport{ReportID: event.ID, ReportHash: reportHash,
		WorkspaceHash: workspaceHash, DiffHash: diffHash,
		ProducedAfterEventID: "", VerifierID: verifierID,
		Passed: passed, LedgerHash: event.Hash}, nil
}
