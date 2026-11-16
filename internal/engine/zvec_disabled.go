//go:build !purego

package engine

import (
	"errors"

	"github.com/frontier-ai-next/Um-mem/internal/store"
)

func openZvecBackend(Config) (store.NativeProjector, nativeRecaller, error) {
	return nil, nil, errors.New("Zvec Go backend requires a purego build and the Zvec C API library")
}
