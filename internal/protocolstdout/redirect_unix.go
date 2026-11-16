//go:build linux || darwin || freebsd

package protocolstdout

import (
	"os"

	"golang.org/x/sys/unix"
)

var retainedStdout *os.File

// Reserve keeps Go's protocol writer on a duplicate of the original stdout
// while C/C++ diagnostics use stderr through file descriptor 1.
func Reserve() error {
	protocolFD, err := unix.Dup(1)
	if err != nil {
		return err
	}
	if err := unix.Dup2(2, 1); err != nil {
		_ = unix.Close(protocolFD)
		return err
	}
	retainedStdout = os.Stdout
	os.Stdout = os.NewFile(uintptr(protocolFD), "um-mem-protocol-stdout")
	return nil
}
