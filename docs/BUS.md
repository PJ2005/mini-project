# NATS Message Bus

## Purpose

`internal/bus/bus.go` abstracts the message bus behind a `MessageBus` interface so the rest of InterLink never imports NATS directly. Adapters publish canonical messages; consumers subscribe by subject pattern.

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

### Request/Reply flow (Fix 6)

```
replyData, err := bus.Request("iot.command.sensor-42", data, 5*time.Second)
```

## Interface

```go
type MessageBus interface {
    Publish(subject string, data []byte) error
    Subscribe(subject string, handler Handler) (Subscription, error)
    Request(subject string, data []byte, timeout time.Duration) ([]byte, error)
    IsConnected() bool
    Close()
}
```

## Implementations

### NATSBus (default)

The standard NATS implementation in `bus.go`. Features:
- `Drain` on close for flushing in-flight messages
- Disconnect/reconnect handlers with structured logging
- `MaxReconnects(-1)` for infinite reconnection
- `Request()` for synchronous command acknowledgment

### JetStreamBus (opt-in)

The JetStream implementation in `jetstream.go`. Enabled via `nats.jetstream: true` in config. Features:
- Creates/reuses `INTERLINK` stream with subjects `iot.>`
- Durable subscriptions for message replay
- Offline subscribers can catch up on missed messages
- Same `MessageBus` interface — transparent to adapters

## Retry + Dead-Letter (Fix 12)

The `PublishWithRetry()` helper attempts publish up to 4 times with exponential backoff (0ms, 100ms, 200ms, 400ms). If all attempts fail, the message is written to the `dead_letters` SQLite table via the registry.

## Key Decisions

1. **Interface + concrete types** — `MessageBus` is the contract; `NATSBus` and `JetStreamBus` are the two implementations.
2. **`Drain` on close** — flushes in-flight messages before disconnecting.
3. **`Handler` signature is `(subject, []byte)`** — keeps handlers transport-agnostic.
4. **`Subscription` interface** — wraps `*nats.Subscription` without exposing it.
5. **Reconnection handlers** — `SetReconnectHandler`/`SetDisconnectHandler` enable the gateway to log connection events and take action.
