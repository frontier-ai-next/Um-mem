package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/localservice"
)

func siblingExecutable(base string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := base
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	sibling := filepath.Join(filepath.Dir(self), name)
	if info, err := os.Stat(sibling); err == nil && !info.IsDir() {
		return sibling, nil
	}
	if found, err := exec.LookPath(name); err == nil {
		return found, nil
	}
	return "", fmt.Errorf("%s executable is not installed beside um-memory or on PATH", base)
}

func startDashboardService(identity localservice.Identity) error {
	executable, err := siblingExecutable("um-memory-service")
	if err != nil {
		return err
	}
	command := exec.Command(executable, "--repo-root", identity.RepoRoot,
		"--workspace", identity.WorkspaceID, "--profile", identity.ProfileID,
		"--backend", identity.Backend, "--owner-pid", "0")
	output, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer output.Close()
	command.Stdin, command.Stdout, command.Stderr = nil, output, output
	hideDashboardServiceWindow(command)
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func dashboardClient(ctx context.Context, identity localservice.Identity,
	stop bool) (*localservice.Client, error) {
	client, err := localservice.NewClient(identity, "operator", 10*time.Second)
	if err == nil || stop {
		return client, err
	}
	paths, pathErr := localservice.Paths(identity)
	if pathErr != nil {
		return nil, pathErr
	}
	if _, statErr := os.Stat(paths.Registry); statErr == nil {
		return nil, err
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}
	if err := startDashboardService(identity); err != nil {
		return nil, err
	}
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errors.New("Go dashboard service did not start within 20 seconds")
		case <-ticker.C:
			client, err = localservice.NewClient(identity, "operator", 10*time.Second)
			if err == nil {
				return client, nil
			}
		}
	}
}

func openDashboardBrowser(rawURL string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "windows":
		name, args = "rundll32.exe", []string{"url.dll,FileProtocolHandler", rawURL}
	case "darwin":
		name, args = "open", []string{rawURL}
	default:
		name, args = "xdg-open", []string{rawURL}
	}
	command := exec.Command(name, args...)
	if err := command.Start(); err != nil {
		return fmt.Errorf("dashboard browser could not be opened: %w", err)
	}
	return command.Process.Release()
}

func dashboardCommand(ctx context.Context, identity localservice.Identity,
	stop, openBrowser bool) (map[string]any, error) {
	client, err := dashboardClient(ctx, identity, stop)
	if err != nil {
		if stop && errors.Is(err, os.ErrNotExist) {
			return map[string]any{"ok": true, "state": "stopped"}, nil
		}
		return nil, err
	}
	defer client.Close()
	result, err := client.Dashboard(ctx, stop)
	if err != nil || result["ok"] != true || stop || !openBrowser {
		return result, err
	}
	if err := openDashboardBrowser(result["url"].(string)); err != nil {
		result["browser_opened"] = false
		result["browser_error"] = err.Error()
	} else {
		result["browser_opened"] = true
	}
	return result, nil
}
