//go:build purego

package engine

import (
	"context"

	"github.com/frontier-ai-next/Um-mem/internal/native/zvec"
	"github.com/frontier-ai-next/Um-mem/internal/store"
)

func openZvecBackend(config Config) (store.NativeProjector, nativeRecaller, error) {
	projector, err := zvec.NewProjector(context.Background(), config.RepoRoot, config.miniLMDir())
	if err != nil {
		return nil, nil, err
	}
	return projector, projector, nil
}
