//go:build !windows && !linux && !darwin && !freebsd

package protocolstdout

import "errors"

func Reserve() error {
	return errors.New("native stdout isolation is unsupported on this platform")
}
