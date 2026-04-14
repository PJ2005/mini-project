package bus

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	nserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"interlink/internal/registry"
)

type flakyBus struct {
	mu       sync.Mutex
	failures int
	attempts int
}

func (f *flakyBus) Publish(string, []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.attempts <= f.failures {
		return errors.New("publish failed")
	}
	return nil
}

func (f *flakyBus) Subscribe(string, Handler) (Subscription, error)       { return nil, nil }
func (f *flakyBus) Request(string, []byte, time.Duration) ([]byte, error) { return nil, nil }
func (f *flakyBus) IsConnected() bool                                     { return true }
func (f *flakyBus) Close()                                                {}

func startTestNATSServer(t *testing.T, jetstream bool) *nserver.Server {
	t.Helper()
	opts := &nserver.Options{Port: -1, JetStream: jetstream, StoreDir: t.TempDir()}
	s, err := nserver.NewServer(opts)
	if err != nil {
		t.Fatalf("new nats server: %v", err)
	}
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats server not ready")
	}
	return s
}

func TestPublishWithRetrySuccessAndDeadLetter(t *testing.T) {
	reg, err := registry.New(filepath.Join(t.TempDir(), "dlq.db"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer reg.Close()

	b1 := &flakyBus{failures: 2}
	if err := PublishWithRetry(b1, "iot.test.dev", []byte("ok"), reg); err != nil {
		t.Fatalf("publish with retry success expected: %v", err)
	}

	b2 := &flakyBus{failures: 10}
	if err := PublishWithRetry(b2, "iot.test.dev", []byte("fail"), reg); err == nil {
		t.Fatal("expected publish failure after retries")
	}
	if n, err := reg.DeadLetterCount(); err != nil || n != 1 {
		t.Fatalf("dead letter count = %d err=%v", n, err)
	}
}

func TestNATSBusRoundTrip(t *testing.T) {
	s := startTestNATSServer(t, false)
	defer s.Shutdown()

	b, err := Connect(s.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer b.Close()

	recv := make(chan []byte, 1)
	sub, err := b.Subscribe("test.subject", func(_ string, data []byte) { recv <- append([]byte(nil), data...) })
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	if err := b.Publish("test.subject", []byte("hello")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case got := <-recv:
		if string(got) != "hello" {
			t.Fatalf("received = %q, want %q", string(got), "hello")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for publish")
	}

	_, err = b.conn.Subscribe("rpc.echo", func(m *nats.Msg) {
		_ = m.Respond([]byte("ack"))
	})
	if err != nil {
		t.Fatalf("subscribe rpc: %v", err)
	}
	resp, err := b.Request("rpc.echo", []byte("x"), 2*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if string(resp) != "ack" {
		t.Fatalf("request response = %q, want %q", string(resp), "ack")
	}

	if !b.IsConnected() {
		t.Fatal("expected connected bus")
	}
	b.SetReconnectHandler(func() {})
	b.SetDisconnectHandler(func(error) {})
}

func TestJetStreamBusRoundTrip(t *testing.T) {
	s := startTestNATSServer(t, true)
	defer s.Shutdown()

	b, err := ConnectJetStream(s.ClientURL())
	if err != nil {
		t.Fatalf("connect jetstream: %v", err)
	}
	defer b.Close()

	if _, err := b.Subscribe("iot.telemetry.>", func(_ string, _ []byte) {}); err == nil {
		t.Fatal("expected subscribe error for current durable-name construction")
	}

	if err := b.Publish("iot.telemetry.dev-1", []byte("42")); err != nil {
		t.Fatalf("jetstream publish: %v", err)
	}

	_, err = b.conn.Subscribe("rpc.js", func(m *nats.Msg) {
		_ = m.Respond([]byte("js-ack"))
	})
	if err != nil {
		t.Fatalf("jetstream rpc subscribe: %v", err)
	}
	resp, err := b.Request("rpc.js", []byte("x"), 2*time.Second)
	if err != nil {
		t.Fatalf("jetstream request: %v", err)
	}
	if string(resp) != "js-ack" {
		t.Fatalf("jetstream response = %q, want %q", string(resp), "js-ack")
	}

	if !b.IsConnected() {
		t.Fatal("expected connected jetstream bus")
	}
	b.SetReconnectHandler(func() {})
	b.SetDisconnectHandler(func(error) {})
}
