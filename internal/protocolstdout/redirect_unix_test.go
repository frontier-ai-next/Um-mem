//go:build linux || darwin || freebsd

package protocolstdout

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestReserveSeparatesNativeAndProtocolOutput(t *testing.T) {
	if os.Getenv("UM_MEM_PROTOCOL_STDOUT_CHILD") == "1" {
		if err := Reserve(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if _, err := unix.Write(1, []byte("native diagnostic\n")); err != nil {
			os.Exit(3)
		}
		if _, err := fmt.Fprintln(os.Stdout, `{"ok":true}`); err != nil {
			os.Exit(4)
		}
		os.Exit(0)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestReserveSeparatesNativeAndProtocolOutput$")
	command.Env = append(os.Environ(), "UM_MEM_PROTOCOL_STDOUT_CHILD=1")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("redirect child: %v, stdout=%q, stderr=%q", err, stdout.String(), stderr.String())
	}
	if stdout.String() != "{\"ok\":true}\n" || !strings.Contains(stderr.String(), "native diagnostic") {
		t.Fatalf("native diagnostics crossed protocol stdout: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}
