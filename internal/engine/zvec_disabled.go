//go:build !purego

package engine

import (
	"errors"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
)

func openZvecBackend(Config) (store.NativeProjector, nativeRecaller, error) {
	return nil, nil, errors.New("Zvec Go backend requires a purego build and the Zvec C API library")
}
