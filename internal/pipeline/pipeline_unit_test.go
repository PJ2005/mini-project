package pipeline

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"interlink/internal/bus"
	"interlink/internal/canonical"
	"interlink/internal/registry"
)

type mockSub struct{}

func (mockSub) Unsubscribe() error { return nil }

type mockBus struct {
	mu        sync.Mutex
	handler   bus.Handler
	published []struct {
		subject string
		data    []byte
	}
}

func (m *mockBus) Publish(subject string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.published = append(m.published, struct {
		subject string
		data    []byte
	}{subject: subject, data: append([]byte(nil), data...)})
	return nil
}

func (m *mockBus) Subscribe(_ string, handler bus.Handler) (bus.Subscription, error) {
	m.handler = handler
	return mockSub{}, nil
}

func (m *mockBus) Request(string, []byte, time.Duration) ([]byte, error) { return nil, nil }
func (m *mockBus) IsConnected() bool                                     { return true }
func (m *mockBus) Close()                                                {}

func TestPipelineHelpers(t *testing.T) {
	if _, err := sourceSubject(SourceConfig{Type: "nats"}); err == nil {
		t.Fatal("expected nats source validation error")
	}
	if got, err := sourceSubject(SourceConfig{Type: "mqtt", Topic: "devices/#"}); err != nil || got != "devices.>" {
		t.Fatalf("mqtt source subject got=%q err=%v", got, err)
	}
	if err := validateSink(SinkConfig{Type: "http_post", URL: "http://x"}); err != nil {
		t.Fatalf("validate sink http_post: %v", err)
	}
	if err := validateSink(SinkConfig{Type: "nats_publish", Subject: "out"}); err != nil {
		t.Fatalf("validate sink nats_publish: %v", err)
	}
	if _, _, ok := resolveOutput(Record{"value": 7}, "temp"); !ok {
		t.Fatal("resolveOutput should find value")
	}
}

func TestPipelineStatsAndDispatch(t *testing.T) {
	b := &mockBus{}
	r, err := registry.New(filepath.Join(t.TempDir(), "pipeline.db"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer r.Close()

	p, err := New(PipelineConfig{
		Name:   "unit",
		Source: SourceConfig{Type: "nats", Subject: "in"},
		Sink:   SinkConfig{Type: "nats_publish", Subject: "out"},
	}, b, r)
	if err != nil {
		t.Fatalf("new pipeline: %v", err)
	}

	msg := canonical.NewTelemetryMessage("dev-1", "nats", "temp", 10, "C")
	data, _ := canonical.MarshalPooled(msg)
	p.handleMessage(sourceMessage{subject: "in", data: data})

	s := p.Stats()
	if s.MessagesProcessed != 1 {
		t.Fatalf("messages processed = %d, want 1", s.MessagesProcessed)
	}
	if s.LastMessageAt == 0 {
		t.Fatal("last message timestamp should be set")
	}
	b.mu.Lock()
	pubs := len(b.published)
	b.mu.Unlock()
	if pubs != 1 {
		t.Fatalf("published = %d, want 1", pubs)
	}
}

func TestPipelineHTTPDispatch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()

	b := &mockBus{}
	r, _ := registry.New(filepath.Join(t.TempDir(), "pipeline.db"))
	defer r.Close()
	p, err := New(PipelineConfig{
		Name:   "http",
		Source: SourceConfig{Type: "nats", Subject: "in"},
		Sink:   SinkConfig{Type: "http_post", URL: ts.URL, Timeout: time.Second},
	}, b, r)
	if err != nil {
		t.Fatalf("new pipeline: %v", err)
	}
	if err := p.dispatch("dev", "temp", 12.5, "C"); err != nil {
		t.Fatalf("dispatch http_post: %v", err)
	}
}

func TestPipelineNewScriptCompileError(t *testing.T) {
	b := &mockBus{}
	r, _ := registry.New(filepath.Join(t.TempDir(), "pipeline.db"))
	defer r.Close()
	_, err := New(PipelineConfig{
		Name:       "bad-script",
		Source:     SourceConfig{Type: "nats", Subject: "in"},
		Transforms: []TransformConfig{{Type: "script", Script: "@@@"}},
		Sink:       SinkConfig{Type: "nats_publish", Subject: "out"},
	}, b, r)
	if err == nil {
		t.Fatal("expected script compile error")
	}
}

func TestPipelineStartStop(t *testing.T) {
	b := &mockBus{}
	r, _ := registry.New(filepath.Join(t.TempDir(), "pipeline.db"))
	defer r.Close()
	p, err := New(PipelineConfig{Name: "lifecycle", Source: SourceConfig{Type: "nats", Subject: "in"}, Sink: SinkConfig{Type: "nats_publish", Subject: "out"}}, b, r)
	if err != nil {
		t.Fatalf("new pipeline: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("start pipeline: %v", err)
	}
	if b.handler == nil {
		t.Fatal("expected subscribe handler to be set")
	}
	p.Stop(context.Background())
	if err := p.dispatch("dev", "temp", 1, "C"); errors.Is(err, context.Canceled) {
		t.Fatal("dispatch should not return context canceled")
	}
}
