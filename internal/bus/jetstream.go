package bus

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

// JetStreamBus implements the MessageBus interface using NATS JetStream.
type JetStreamBus struct {
	conn *nats.Conn
	js   nats.JetStreamContext
	reconnectHandler  func()
	disconnectHandler func(error)
}

const (
	streamName     = "INTERLINK"
	streamSubjects = "iot.>"
)

// ConnectJetStream connects to NATS and initialises JetStream, creating or
// updating the INTERLINK stream as needed.
func ConnectJetStream(url string) (*JetStreamBus, error) {
	b := &JetStreamBus{}

	nc, err := nats.Connect(url,
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			slog.Warn("NATS disconnected", "component", "bus/jetstream", "error", err)
			if b.disconnectHandler != nil {
				b.disconnectHandler(err)
			}
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			slog.Info("NATS reconnected", "component", "bus/jetstream")
			if b.reconnectHandler != nil {
				b.reconnectHandler()
			}
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("nats connect %s: %w", url, err)
	}

	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream init: %w", err)
	}

	_, err = js.AddStream(&nats.StreamConfig{
		Name:     streamName,
		Subjects: []string{streamSubjects},
		Storage:  nats.FileStorage,
	})
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("jetstream add stream: %w", err)
	}
	slog.Info("JetStream stream ready", "component", "bus/jetstream", "stream", streamName)

	b.conn = nc
	b.js = js
	return b, nil
}

func (b *JetStreamBus) Publish(subject string, data []byte) error {
	_, err := b.js.Publish(subject, data)
	return err
}

func (b *JetStreamBus) Subscribe(subject string, handler Handler) (Subscription, error) {
	durable := "interlink-" + subject
	sub, err := b.js.Subscribe(subject, func(msg *nats.Msg) {
		handler(msg.Subject, msg.Data)
		msg.Ack()
	}, nats.Durable(durable), nats.DeliverAll())
	if err != nil {
		return nil, fmt.Errorf("jetstream subscribe %s: %w", subject, err)
	}
	return sub, nil
}

func (b *JetStreamBus) Request(subject string, data []byte, timeout time.Duration) ([]byte, error) {
	msg, err := b.conn.Request(subject, data, timeout)
	if err != nil {
		return nil, err
	}
	return msg.Data, nil
}

func (b *JetStreamBus) IsConnected() bool {
	return b.conn != nil && b.conn.IsConnected()
}

func (b *JetStreamBus) Close() {
	b.conn.Drain()
}

func (b *JetStreamBus) SetReconnectHandler(fn func()) {
	b.reconnectHandler = fn
}

func (b *JetStreamBus) SetDisconnectHandler(fn func(error)) {
	b.disconnectHandler = fn
}
