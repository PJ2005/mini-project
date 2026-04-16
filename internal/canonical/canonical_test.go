package canonical

import (
	"strings"
	"testing"
)

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

func TestMarshalRoundTripAndSubject(t *testing.T) {
	in := NewTelemetryMessage("dev-1", "mqtt", "temperature", 21.25, "C")
	data, err := Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("marshal returned empty data")
	}

	out, err := UnmarshalMessage(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := out.GetDeviceId(); got != "dev-1" {
		t.Fatalf("device_id = %q, want %q", got, "dev-1")
	}
	if got := out.GetTelemetry().GetMetric(); got != "temperature" {
		t.Fatalf("metric = %q, want %q", got, "temperature")
	}
	if got := out.GetTelemetry().GetValue(); got != 21.25 {
		t.Fatalf("value = %v, want %v", got, 21.25)
	}

	if got := SubjectType(out); got != "telemetry" {
		t.Fatalf("subject type = %q, want %q", got, "telemetry")
	}
	if got := Subject(out); got != "iot.telemetry.dev-1" {
		t.Fatalf("subject = %q, want %q", got, "iot.telemetry.dev-1")
	}
}

func TestMarshalPooledRoundTrip(t *testing.T) {
	msg := NewCommandMessage("dev-2", "http", "reboot", []byte(`{"delay":2}`))
	data, err := MarshalPooled(msg)
	if err != nil {
		t.Fatalf("marshal pooled: %v", err)
	}
	out, err := UnmarshalMessage(data)
	if err != nil {
		t.Fatalf("unmarshal pooled data: %v", err)
	}
	if got := out.GetCommand().GetAction(); got != "reboot" {
		t.Fatalf("command action = %q, want %q", got, "reboot")
	}
}

func TestEventAndUnknownSubjectType(t *testing.T) {
	event := NewEventMessage("dev-3", "coap", "alarm", "high", "threshold exceeded")
	if got := SubjectType(event); got != "event" {
		t.Fatalf("subject type = %q, want %q", got, "event")
	}
	if got := Subject(event); got != "iot.event.dev-3" {
		t.Fatalf("subject = %q, want %q", got, "iot.event.dev-3")
	}

	unknown := &Message{DeviceId: "dev-x"}
	if got := SubjectType(unknown); got != "unknown" {
		t.Fatalf("subject type = %q, want %q", got, "unknown")
	}
}

func TestUnmarshalMessageError(t *testing.T) {
	_, err := UnmarshalMessage([]byte("not-protobuf"))
	if err == nil {
		t.Fatal("expected unmarshal error, got nil")
	}
	if !strings.Contains(err.Error(), "unmarshal canonical message") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestConstructorsPopulateFields(t *testing.T) {
	cmd := NewCommandMessage("dev-9", "mqtt", "restart", []byte(`{"x":1}`))
	if cmd.GetTimestampMs() <= 0 {
		t.Fatalf("timestamp = %d, want > 0", cmd.GetTimestampMs())
	}
	if got := cmd.GetSourceProto(); got != "mqtt" {
		t.Fatalf("source proto = %q, want %q", got, "mqtt")
	}
	if got := cmd.GetCommand().GetAction(); got != "restart" {
		t.Fatalf("action = %q, want %q", got, "restart")
	}

	tel := NewTelemetryMessage("dev-10", "http", "humidity", 55.5, "%")
	if got := tel.GetTelemetry().GetUnit(); got != "%" {
		t.Fatalf("unit = %q, want %q", got, "%")
	}
}
