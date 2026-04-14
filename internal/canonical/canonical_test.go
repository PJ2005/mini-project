package canonical

import "testing"

func TestMessageID(t *testing.T) {
	seen := make(map[string]struct{}, 1000)

	for i := 0; i < 1000; i++ {
		msg := NewTelemetryMessage("device-1", "mqtt", "temp", 21.5, "c")
		id := msg.GetMessageId()
		if len(id) != 32 {
			t.Fatalf("telemetry message id length = %d, want 32", len(id))
		}
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate telemetry message id: %s", id)
		}
		seen[id] = struct{}{}
	}

	if id := NewEventMessage("device-1", "mqtt", "alarm", "warning", "detail").GetMessageId(); len(id) != 32 {
		t.Fatalf("event message id length = %d, want 32", len(id))
	}
	if id := NewCommandMessage("device-1", "mqtt", "reboot", []byte(`{"delay":1}`)).GetMessageId(); len(id) != 32 {
		t.Fatalf("command message id length = %d, want 32", len(id))
	}
}
