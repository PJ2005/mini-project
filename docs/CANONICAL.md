# Canonical Message Schema

## Purpose

The canonical message model (`proto/canonical.proto`) is the single internal contract inside InterLink. Every adapter converts its native format into a `Message` before publishing to the NATS bus. This decouples protocol-specific concerns from routing, policy evaluation, and storage.

The `Message` wrapper carries identity (`message_id`, `device_id`), timing (`timestamp_ms`), origin (`source_proto`), arbitrary key-value `metadata`, and exactly one typed payload via `oneof`:

| Payload | Use case |
|---------|----------|
| `TelemetryPayload` | Periodic sensor readings (metric + value + unit) |
| `CommandPayload` | Actions sent to a device (action + opaque params) |
| `EventPayload` | One-shot occurrences (type + severity + detail) |

Field numbers 10-12 are reserved for the `oneof` payloads; 20 for the metadata map. This leaves room for future top-level fields (5-9) without breaking wire compatibility.

## Key Decisions

1. **`oneof` over `Any`** — typed payloads give compile-time safety and avoid runtime type-URL resolution. Every consumer knows exactly which payload shapes exist.
2. **`timestamp_ms` as int64** — millisecond Unix epoch avoids Protobuf `Timestamp` import and timezone ambiguity. Callers produce it with `time.Now().UnixMilli()`.
3. **`source_proto` as string** — plain string (`"mqtt"`, `"http"`) rather than an enum so new adapters don't require a proto rebuild.
4. **`metadata` map** — open-ended key-value bag for adapter-specific context (e.g. MQTT topic, HTTP headers) without polluting the core schema.
5. **Subject derivation lives in Go, not proto** — `canonical.Subject(msg)` builds `iot.<type>.<device_id>` from the `oneof` case. Keeping this in Go avoids proto-level coupling to NATS.

## How To Extend

- **New payload type** — add a message (e.g. `DiagnosticPayload`), add a case to the `oneof`, assign a field number > 12, and add a case to `SubjectType()` in `canonical.go`.
- **New top-level field** — assign field numbers 5-9 (reserved gap). No existing payloads break.
- **New metadata convention** — no proto change needed. Document the key in this file and use the `metadata` map.
