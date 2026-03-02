# Adapter Interface

## Purpose

Every protocol integration in EdgeMesh is an **adapter** — a struct that satisfies the `Adapter` interface in `internal/adapter/adapter.go`. Adapters are the only place where protocol-specific logic lives. They convert native messages into canonical protobuf, publish to the NATS bus, and register devices in the registry. Adapters never call each other; all communication flows through NATS.

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

## How To Extend — Creating a CoAP Adapter

This walkthrough creates a fictional CoAP (RFC 7252) adapter from scratch.

### Step 1: Create the file

Create `internal/adapter/coap/coap.go`. This is the only file needed.

```
edgemesh/
└── internal/
    └── adapter/
        └── coap/
            └── coap.go   ← new file
```

### Step 2: Define the struct

```go
package coap

import (
    "context"
    "fmt"
    "log"

    "edgemesh/internal/adapter"
    "edgemesh/internal/bus"
    "edgemesh/internal/canonical"
    "edgemesh/internal/registry"
)

type CoAPAdapter struct {
    listenAddr string
    bus        bus.MessageBus
    reg        *registry.Registry
    convert    adapter.ConverterFunc
    cancel     context.CancelFunc
}

func New(listenAddr string) *CoAPAdapter {
    return &CoAPAdapter{
        listenAddr: listenAddr,
        convert:    defaultConverter,
    }
}
```

### Step 3: Implement the Adapter interface

```go
func (a *CoAPAdapter) Name() string { return "coap" }

func (a *CoAPAdapter) Start(ctx context.Context, b bus.MessageBus, reg *registry.Registry) error {
    a.bus = b
    a.reg = reg
    ctx, a.cancel = context.WithCancel(ctx)

    // Start your CoAP server here, listening on a.listenAddr.
    // When a CoAP message arrives, call a.handleMessage(payload, deviceID).

    log.Printf("[coap] listening on %s", a.listenAddr)
    return nil
}

func (a *CoAPAdapter) Stop(ctx context.Context) error {
    if a.cancel != nil {
        a.cancel()
    }
    log.Println("[coap] stopped")
    return nil
}
```

### Step 4: Implement the message handler

This is where you convert the protocol-native message to canonical and publish it.

```go
func (a *CoAPAdapter) handleMessage(raw []byte, deviceID string) {
    data, err := a.convert(raw)
    if err != nil {
        log.Printf("[coap] convert error: %v", err)
        return
    }

    msg, err := canonical.UnmarshalMessage(data)
    if err != nil {
        log.Printf("[coap] unmarshal error: %v", err)
        return
    }

    if err := a.bus.Publish(canonical.Subject(msg), data); err != nil {
        log.Printf("[coap] publish error: %v", err)
        return
    }

    a.reg.Register(registry.Device{
        DeviceID: deviceID,
        Name:     fmt.Sprintf("coap-%s", deviceID),
        Protocol: "coap",
        Status:   "active",
    })
}
```

### Step 5: Write the converter

```go
func defaultConverter(raw []byte) ([]byte, error) {
    // Parse CoAP payload, extract device_id and sensor data,
    // then build a canonical message:
    msg := canonical.NewTelemetryMessage(
        "device-from-coap",
        "coap",
        "temperature",
        22.5,
        "celsius",
    )
    return canonical.Marshal(msg)
}
```

### Step 6: Wire it into the gateway

In `internal/gateway/gateway.go` (future phase), add:

```go
coapAdapter := coap.New(":5683")
adapters = append(adapters, coapAdapter)
```

No other files change. The gateway calls `Start(ctx, bus, registry)` on every adapter uniformly.
