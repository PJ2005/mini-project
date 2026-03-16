# Adapter Interface

## Purpose

Every protocol integration in InterLink is an **adapter** — a struct that satisfies the `Adapter` interface in `internal/adapter/adapter.go`. Adapters are the only place where protocol-specific logic lives. They convert native messages into canonical protobuf, publish to the NATS bus, and register devices in the registry. Adapters never call each other; all communication flows through NATS.

The interface:

```go
type Adapter interface {
    Name() string
    Start(ctx context.Context, b bus.MessageBus, reg *registry.Registry) error
    Stop(ctx context.Context) error
}
```

`ConverterFunc` is a function type that transforms raw protocol bytes into a serialized canonical `Message`:

```go
type ConverterFunc func(raw []byte) ([]byte, error)
```

## Key Decisions

1. **Three-method interface** — `Name` for logging/identification, `Start` receives all shared dependencies, `Stop` for graceful shutdown. No Init phase; construction happens in the adapter's own `New()` function.
2. **Bus and Registry injected via Start** — adapters don't create their own connections. The gateway wires these and passes them in, making adapters stateless until started.
3. **ConverterFunc is a type, not part of the interface** — conversion logic varies wildly between protocols. Some adapters parse JSON, others binary frames. A typed function makes conversion composable without forcing every adapter to expose it.
4. **Structured logging** — all adapters use `log/slog` with a `"component"` field matching the adapter name for consistent, filterable log output.

## How To Extend — Creating a New Adapter

This walkthrough creates a fictional Modbus adapter from scratch.

### Step 1: Create the file

Create `internal/adapter/modbus/modbus.go`. This is the only file needed.

```
interlink/
└── internal/
    └── adapter/
        └── modbus/
            └── modbus.go   ← new file
```

### Step 2: Define the struct

```go
package modbus

import (
    "context"
    "log/slog"

    "interlink/internal/bus"
    "interlink/internal/canonical"
    "interlink/internal/registry"
)

type Config struct {
    Listen string `yaml:"listen"`
}

type Adapter struct {
    cfg    Config
    bus    bus.MessageBus
    reg    *registry.Registry
    cancel context.CancelFunc
}

func New(cfg Config) *Adapter {
    return &Adapter{cfg: cfg}
}
```

### Step 3: Implement the Adapter interface

```go
func (a *Adapter) Name() string { return "modbus" }

func (a *Adapter) Start(ctx context.Context, b bus.MessageBus, reg *registry.Registry) error {
    a.bus = b
    a.reg = reg
    ctx, a.cancel = context.WithCancel(ctx)

    // Start your Modbus listener here.
    // When a Modbus message arrives, call a.handleMessage(payload, deviceID).

    slog.Info("adapter started", "component", "modbus", "listen", a.cfg.Listen)
    return nil
}

func (a *Adapter) Stop(ctx context.Context) error {
    if a.cancel != nil {
        a.cancel()
    }
    slog.Info("adapter stopped", "component", "modbus")
    return nil
}
```

### Step 4: Implement the message handler

```go
func (a *Adapter) handleMessage(deviceID string, metric string, value float64) {
    msg := canonical.NewTelemetryMessage(deviceID, "modbus", metric, value, "")
    data, err := canonical.Marshal(msg)
    if err != nil {
        slog.Error("marshal error", "component", "modbus", "error", err)
        return
    }

    if err := a.bus.Publish(canonical.Subject(msg), data); err != nil {
        slog.Error("publish error", "component", "modbus", "error", err)
        return
    }

    a.reg.Register(registry.Device{
        DeviceID: deviceID,
        Name:     deviceID,
        Protocol: "modbus",
        Status:   "active",
    })
}
```

### Step 5: Wire it into the gateway

In `internal/gateway/gateway.go`, add:

```go
import adaptmodbus "interlink/internal/adapter/modbus"

// In Run(), add to the adapters slice:
adapters := []adapter.Adapter{
    adaptmqtt.New(cfg.MQTT),
    httpAdapter,
    adaptcoap.New(cfg.CoAP),
    adaptmodbus.New(cfg.Modbus),  // ← new
}
```

No other files change. The gateway calls `Start(ctx, bus, registry)` on every adapter uniformly.
