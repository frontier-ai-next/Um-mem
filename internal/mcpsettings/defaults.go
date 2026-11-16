package mcpsettings

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// DefaultRepoRoot stores memory in user data, never in the source checkout.
func DefaultRepoRoot() (string, error) {
	if runtime.GOOS == "windows" {
		if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
			return filepath.Abs(filepath.Join(local, "UM-MEM", "store"))
		}
	}
	if data := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); data != "" {
		return filepath.Abs(filepath.Join(data, "um-mem", "store"))
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(home, ".local", "share", "um-mem", "store"))
}

func DefaultSettingsPath(host string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch host {
	case "claude":
		return filepath.Abs(filepath.Join(home, ".claude.json"))
	case "codex":
		codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
		if codexHome == "" {
			codexHome = filepath.Join(home, ".codex")
		}
		return filepath.Abs(filepath.Join(codexHome, "config.toml"))
	case "hermes":
		hermesHome := strings.TrimSpace(os.Getenv("HERMES_HOME"))
		if hermesHome == "" && runtime.GOOS == "windows" {
			if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
				hermesHome = filepath.Join(local, "hermes")
			}
		}
		if hermesHome == "" {
			hermesHome = filepath.Join(home, ".hermes")
		}
		return filepath.Abs(filepath.Join(hermesHome, "config.yaml"))
	default:
		return "", errors.New("MCP host must be claude, codex or hermes")
	}
}
