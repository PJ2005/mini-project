package bus

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"edgemesh/internal/metrics"
	"edgemesh/internal/registry"
)

type Handler func(subject string, data []byte)

// MessageBus is the central messaging interface used by the gateway and all adapters.
type MessageBus interface {
	Publish(subject string, data []byte) error
	Subscribe(subject string, handler Handler) (Subscription, error)
	// Request performs a NATS request/reply with the given timeout.
	Request(subject string, data []byte, timeout time.Duration) ([]byte, error)
	// IsConnected reports whether the underlying connection is active.
	IsConnected() bool
	Close()
}

type Subscription interface {
	Unsubscribe() error
}

// ── NATSBus ────────────────────────────────────────────

type NATSBus struct {
	conn               *nats.Conn
	reconnectHandler   func()
	disconnectHandler  func(error)
}

func Connect(url string, opts ...nats.Option) (*NATSBus, error) {
	b := &NATSBus{}

	defaults := []nats.Option{
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			slog.Warn("NATS disconnected", "component", "bus", "error", err)
			if b.disconnectHandler != nil {
				b.disconnectHandler(err)
			}
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			slog.Info("NATS reconnected", "component", "bus")
			metrics.NATSReconnections.Inc()
			if b.reconnectHandler != nil {
				b.reconnectHandler()
			}
		}),
	}

	allOpts := append(defaults, opts...)
	nc, err := nats.Connect(url, allOpts...)
	if err != nil {
		return nil, fmt.Errorf("nats connect %s: %w", url, err)
	}
	b.conn = nc
	return b, nil
}

func (b *NATSBus) Publish(subject string, data []byte) error {
	start := time.Now()
	err := b.conn.Publish(subject, data)
	metrics.NATSPublishDuration.Observe(time.Since(start).Seconds())
	return err
}

func (b *NATSBus) Subscribe(subject string, handler Handler) (Subscription, error) {
	sub, err := b.conn.Subscribe(subject, func(msg *nats.Msg) {
		handler(msg.Subject, msg.Data)
	})
	if err != nil {
		return nil, fmt.Errorf("nats subscribe %s: %w", subject, err)
	}
	return sub, nil
}

func (b *NATSBus) Request(subject string, data []byte, timeout time.Duration) ([]byte, error) {
	msg, err := b.conn.Request(subject, data, timeout)
	if err != nil {
		return nil, err
	}
	return msg.Data, nil
}

func (b *NATSBus) IsConnected() bool {
	return b.conn != nil && b.conn.IsConnected()
}

func (b *NATSBus) Close() {
	b.conn.Drain()
}

// SetReconnectHandler registers a callback invoked when NATS reconnects.
func (b *NATSBus) SetReconnectHandler(fn func()) {
	b.reconnectHandler = fn
}

// SetDisconnectHandler registers a callback invoked when NATS disconnects.
func (b *NATSBus) SetDisconnectHandler(fn func(error)) {
	b.disconnectHandler = fn
}

// ── Retry with Dead-Letter (Fix 12) ───────────────────

// PublishWithRetry attempts to publish a message up to 3 times with exponential
// backoff (100ms, 200ms, 400ms). If all attempts fail, the message is written
// to the dead-letter table in the registry.
func PublishWithRetry(b MessageBus, subject string, data []byte, reg *registry.Registry) error {
	backoffs := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}
	var lastErr error

	// First attempt (no delay).
	if err := b.Publish(subject, data); err == nil {
		return nil
	} else {
		lastErr = err
		slog.Warn("publish attempt failed", "component", "bus", "attempt", 1, "subject", subject, "error", err)
	}

	// Retry attempts with backoff.
	for i, wait := range backoffs {
		time.Sleep(wait)
		if err := b.Publish(subject, data); err == nil {
			return nil
		} else {
			lastErr = err
			slog.Warn("publish attempt failed", "component", "bus", "attempt", i+2, "subject", subject, "error", err)
		}
	}

	// All retries exhausted — write to dead-letter table.
	slog.Error("all publish attempts exhausted; writing to dead-letter table", "component", "bus", "subject", subject)
	if reg != nil {
		if dlErr := reg.InsertDeadLetter(subject, data, lastErr.Error()); dlErr != nil {
			slog.Error("failed to write dead letter", "component", "bus", "error", dlErr)
		}
	}
	return fmt.Errorf("publish failed after 4 attempts: %w", lastErr)
}
