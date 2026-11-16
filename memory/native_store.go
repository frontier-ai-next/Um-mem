package memory

// MemPalaceStoreForBlock is the persisted write-outbox and native collection
// family selected by the Python MemPalace adapter.
func MemPalaceStoreForBlock(record MemoryRecord) string {
	switch record.Kind {
	case "durable", "verification_report":
		return "palace.durable"
	case "lesson", "bkb_rule":
		return "palace.lesson"
	case "codeptr":
		return "palace.codeptr"
	case "skill_index":
		return "palace.skill_index"
	case "run_state":
		return "palace.run"
	}
	if record.DeliveryPolicy == "working_set" {
		return "palace.run"
	}
	return "palace.idea"
}
