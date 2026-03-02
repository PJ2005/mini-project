package bus

import (
	"fmt"

	"github.com/nats-io/nats.go"
)

type Handler func(subject string, data []byte)

type MessageBus interface {
	Publish(subject string, data []byte) error
	Subscribe(subject string, handler Handler) (Subscription, error)
	Close()
}

type Subscription interface {
	Unsubscribe() error
}

type NATSBus struct {
	conn *nats.Conn
}

func Connect(url string) (*NATSBus, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("nats connect %s: %w", url, err)
	}
	return &NATSBus{conn: nc}, nil
}

func (b *NATSBus) Publish(subject string, data []byte) error {
	return b.conn.Publish(subject, data)
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

func (b *NATSBus) Close() {
	b.conn.Drain()
}
