package policy

import (
	"context"
	"os"
	"path/filepath"
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

func TestEvaluateAllowAndDeny(t *testing.T) {
	allow := New(Config{
		DefaultAction: "deny",
		Rules: []Rule{{
			DevicePattern: "sensor-*",
			SourceProto:   "mqtt",
			MessageType:   "telemetry",
			Action:        "allow",
		}},
	})
	if ok := allow.Evaluate(canonical.NewTelemetryMessage("sensor-1", "mqtt", "temp", 10, "C")); !ok {
		t.Fatal("expected allow rule to pass")
	}

	deny := New(Config{
		DefaultAction: "allow",
		Rules: []Rule{{
			DevicePattern: "sensor-*",
			SourceProto:   "mqtt",
			MessageType:   "telemetry",
			Action:        "deny",
		}},
	})
	if ok := deny.Evaluate(canonical.NewTelemetryMessage("sensor-1", "mqtt", "temp", 10, "C")); ok {
		t.Fatal("expected deny rule to block")
	}
}

func TestSubmitWithoutAndWithFullBuffer(t *testing.T) {
	e := New(Config{DefaultAction: "allow"})
	if ok := e.Submit("iot.telemetry.dev", []byte("x")); ok {
		t.Fatal("expected submit to fail when worker pool is not started")
	}

	e.submitCh = make(chan policyWorkItem, 1)
	e.submitCh <- policyWorkItem{subject: "full", data: []byte("x")}
	if ok := e.Submit("iot.telemetry.dev", []byte("y")); ok {
		t.Fatal("expected submit to fail on full buffer")
	}
}

func TestReloadUpdatesRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte("policy:\n  default_action: deny\n  rules:\n    - device_pattern: \"sensor-*\"\n      source_proto: \"mqtt\"\n      message_type: \"telemetry\"\n      action: allow\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	e := New(Config{DefaultAction: "allow"})
	e.reload(path)

	if ok := e.Evaluate(canonical.NewTelemetryMessage("sensor-9", "mqtt", "temp", 10, "C")); !ok {
		t.Fatal("expected reloaded allow rule to pass")
	}
	if ok := e.Evaluate(canonical.NewTelemetryMessage("other", "mqtt", "temp", 10, "C")); ok {
		t.Fatal("expected default deny after reload")
	}
}
