package engine

import "gitlab.frontierai.ru/home/frontierai/teams/mag/frontier-memory/umbrella-memory/um-mem-module/memory"

// recordNativeStore matches the persisted write-outbox store selected for the
// record. MemPalace journals a collection family, not its backend selector.
func (engine *Engine) recordNativeStore(record memory.MemoryRecord) string {
	if engine.config.BackendID == "mempalace" {
		return memory.MemPalaceStoreForBlock(record)
	}
	return engine.config.BackendID
}
