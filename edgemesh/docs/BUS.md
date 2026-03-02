# NATS Message Bus

## Purpose

`internal/bus/bus.go` abstracts the message bus behind a `MessageBus` interface so the rest of EdgeMesh never imports NATS directly. Adapters publish canonical messages; consumers subscribe by subject pattern.

Subject pattern: **`iot.<type>.<device_id>`**

| Segment | Values | Example |
|---------|--------|---------|
| `type` | `telemetry`, `command`, `event` | `iot.telemetry.sensor-42` |
| `device_id` | device registry ID | `iot.event.gateway-01` |

Wildcards follow standard NATS syntax:
- `iot.telemetry.*` — all telemetry from any device
- `iot.>` — everything in the `iot` tree

### Publish flow

```
Adapter → canonical.Marshal(msg) → bus.Publish(canonical.Subject(msg), data)
```

### Subscribe flow

```
bus.Subscribe("iot.telemetry.*", func(subject string, data []byte) {
    msg, _ := canonical.UnmarshalMessage(data)
    // process
})
```

## Key Decisions

1. **Interface + concrete type** — `MessageBus` is the contract; `NATSBus` is the only implementation today. Swapping to JetStream or another broker requires only a new struct satisfying `MessageBus`.
2. **`Drain` on close** — `NATSBus.Close()` calls `Drain()` instead of `Close()` to let in-flight messages flush before disconnecting.
3. **`Handler` signature is `(subject, []byte)`** — keeps handlers transport-agnostic. The handler receives raw bytes and calls `canonical.UnmarshalMessage` itself, avoiding a bus-level dependency on the proto package.
4. **`Subscription` interface** — returned by `Subscribe` so callers can unsubscribe cleanly. Wraps `*nats.Subscription` without exposing it.

## How To Extend

- **Request/reply** — add `Request(subject string, data []byte, timeout time.Duration) ([]byte, error)` to the `MessageBus` interface and implement with `nc.Request`.
- **JetStream** — create a `JetStreamBus` struct that satisfies `MessageBus`. Use `nats.JetStreamContext` internally.
- **Metrics** — wrap `NATSBus` in a decorator that increments counters on `Publish`/`Subscribe` calls.
