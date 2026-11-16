package hosthook

import (
	"os"
	"path/filepath"
)

// GitRoot follows the host hook's closest containing repository boundary.
// A worktree .git file counts as a repository marker too.
func GitRoot(workingDir string) string {
	directory, err := filepath.Abs(workingDir)
	if err != nil {
		return workingDir
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, ".git")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return workingDir
		}
		directory = parent
	}
}
