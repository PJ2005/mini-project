package adapter

import (
	"context"

	"interlink/internal/bus"
	"interlink/internal/registry"
)

type Adapter interface {
	Name() string
	Start(ctx context.Context, b bus.MessageBus, reg *registry.Registry) error
	Stop(ctx context.Context) error
}

// ConverterFunc transforms raw protocol-specific bytes into a canonical
// protobuf-encoded message. Each adapter supplies its own implementation.
type ConverterFunc func(raw []byte) ([]byte, error)
