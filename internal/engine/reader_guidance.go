package engine

import (
	"errors"

	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/internal/blockcatalog"
	"gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"
)

// CurrentReaderGuidance returns the compiled card for one visible static
// logical block. Its text is diagnostic input, never prompt evidence.
func (engine *Engine) CurrentReaderGuidance(blockID string) (map[string]any, error) {
	blocks, err := blockcatalog.Load(blockcatalog.Options{RepoRoot: engine.config.RepoRoot,
		WorkspaceID: engine.config.WorkspaceID, ProfileID: engine.config.ProfileID,
		Strict: true, IncludeWorkspace: true})
	if err != nil {
		return nil, err
	}
	compiled, err := memory.CompileSemanticCatalog(blocks)
	if err != nil {
		return nil, err
	}
	for _, descriptor := range compiled.Descriptors {
		if descriptor.BlockID == blockID {
			return map[string]any{"block_id": blockID, "guidance": map[string]any{
				"block_id": descriptor.Guidance.BlockID, "synopsis": descriptor.Guidance.Synopsis,
				"use_when": descriptor.Guidance.UseWhen, "avoid_when": descriptor.Guidance.AvoidWhen}}, nil
		}
	}
	return nil, errors.New("deep reader logical block is not in the visible catalog")
}
