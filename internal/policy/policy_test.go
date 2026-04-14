package policy

import (
	"context"
	"sync"
	"testing"
	"time"

	"interlink/internal/bus"
	"interlink/internal/canonical"
)

type publishedMessage struct {
	subject string
	data    []byte
}

type fakeBus struct {
	mu        sync.Mutex
	published []publishedMessage
	ch        chan publishedMessage
}

func newFakeBus() *fakeBus {
	return &fakeBus{ch: make(chan publishedMessage, 1)}
}

func (f *fakeBus) Publish(subject string, data []byte) error {
	msg := publishedMessage{subject: subject, data: append([]byte(nil), data...)}
	f.mu.Lock()
	f.published = append(f.published, msg)
	f.mu.Unlock()
	select {
	case f.ch <- msg:
	default:
	}
	return nil
}

func (f *fakeBus) Subscribe(string, bus.Handler) (bus.Subscription, error) {
	return nil, nil
}

func (f *fakeBus) Request(string, []byte, time.Duration) ([]byte, error) {
	return nil, nil
}

func (f *fakeBus) IsConnected() bool { return true }
func (f *fakeBus) Close()            {}

func TestStartWorkerPoolProcessesSubmittedMessage(t *testing.T) {
	engine := New(Config{
		DefaultAction: "deny",
		Rules: []Rule{{
			DevicePattern: "sensor-*",
			SourceProto:   "mqtt",
			MessageType:   "telemetry",
			Action:        "allow",
			CommandTarget: "actuator-1",
			CommandAction: "adjust",
		}},
	})

	fb := newFakeBus()
	engine.SetBus(fb)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine.StartWorkerPool(ctx, 1)

	msg := canonical.NewTelemetryMessage("sensor-1", "mqtt", "temp", 23.5, "c")
	data, err := canonical.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal telemetry message: %v", err)
	}
	if !engine.Submit(canonical.Subject(msg), data) {
		t.Fatal("submit returned false")
	}

	select {
	case pub := <-fb.ch:
		if pub.subject != "iot.command.actuator-1" {
			t.Fatalf("publish subject = %q, want %q", pub.subject, "iot.command.actuator-1")
		}
		cmd, err := canonical.UnmarshalMessage(pub.data)
		if err != nil {
			t.Fatalf("unmarshal command message: %v", err)
		}
		if got := cmd.GetDeviceId(); got != "actuator-1" {
			t.Fatalf("command device_id = %q, want %q", got, "actuator-1")
		}
		if got := cmd.GetSourceProto(); got != "policy" {
			t.Fatalf("command source_proto = %q, want %q", got, "policy")
		}
		if got := cmd.GetCommand().GetAction(); got != "adjust" {
			t.Fatalf("command action = %q, want %q", got, "adjust")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for policy worker publish")
	}
}
