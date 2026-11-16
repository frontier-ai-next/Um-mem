//go:build !windows

package main

import "os/exec"

func hideDashboardServiceWindow(_ *exec.Cmd) {}
