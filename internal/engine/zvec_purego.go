//go:build purego

package engine

import (
	"context"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/native/zvec"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/store"
)

func openZvecBackend(config Config) (store.NativeProjector, nativeRecaller, error) {
	projector, err := zvec.NewProjector(context.Background(), config.RepoRoot, config.miniLMDir())
	if err != nil {
		return nil, nil, err
	}
	return projector, projector, nil
}
