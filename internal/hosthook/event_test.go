package hosthook

import "testing"

func TestCodexHostStateMatchesPython(t *testing.T) {
	event := Event{HostSlug: "codex", HostLabel: "Codex", AdapterID: "codex_hooks",
		CapabilityCode: "codex_hook", SchemaVersion: 1, EventName: "UserPromptSubmit",
		SessionID: "session-1", StageKind: "turn", StageID: "turn-1"}
	digest, err := event.Digest()
	if err != nil || digest != "1fdbd8507d7f861629773db21c0080bc3a6308447dca8309af78d2698ca80a38" {
		t.Fatalf("Python host hook digest differs: %s, %v", digest, err)
	}
	payload, err := event.HostStatePayload("prospective_trigger")
	if err != nil || payload.EventID != "codex-host-state:"+digest ||
		payload.RunID != "codex-session:84097828fc31a8c8d29210df" ||
		payload.StageID != "codex-turn:974cad2dd603827b06e9e6af" ||
		len(payload.SignalCodes) != 2 || payload.SignalCodes[0] != "codex_event:UserPromptSubmit" {
		t.Fatalf("Python host state payload differs: %+v, %v", payload, err)
	}
}
