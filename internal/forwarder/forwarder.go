package forwarder

import (
	"context"

	"interlink/internal/bus"
)

type Forwarder interface {
	Name() string
	Start(ctx context.Context, b bus.MessageBus) error
	Stop(ctx context.Context) error
}
