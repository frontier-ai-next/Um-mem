//go:build windows

package protocolstdout

import (
	"errors"
	"os"
	"syscall"
)

var retainedStdout *os.File

// Reserve moves the C/C++ runtime's stdout to stderr while keeping Go's
// protocol writer on the original stdout handle. Chroma's HNSW integrity check
// writes directly to std::cout when a populated index is reopened.
func Reserve() error {
	crt := syscall.NewLazyDLL("ucrtbase.dll")
	copyFD, _, callErr := crt.NewProc("_dup").Call(1)
	if int32(copyFD) < 0 {
		return errors.Join(errors.New("duplicate protocol stdout"), callErr)
	}
	handle, _, callErr := crt.NewProc("_get_osfhandle").Call(copyFD)
	if int64(handle) < 0 {
		return errors.Join(errors.New("resolve protocol stdout handle"), callErr)
	}
	protocol := os.NewFile(handle, "um-mem-protocol-stdout")
	if protocol == nil {
		return errors.New("protocol stdout handle is unavailable")
	}
	status, _, callErr := crt.NewProc("_dup2").Call(2, 1)
	if status != 0 {
		return errors.Join(errors.New("redirect native diagnostics to stderr"), callErr)
	}
	retainedStdout = os.Stdout
	os.Stdout = protocol
	return nil
}
