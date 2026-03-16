# Slide 1 — InterLink: Lightweight IoT Interoperability Middleware

- Bridges IoT protocols through a canonical Protobuf model
- Runs at the edge — no cloud dependency, no CGo required
- NATS message bus (with optional JetStream) decouples all protocol adapters
- Pure-Go codebase, single binary, structured JSON logging

NOTE: InterLink exists because most IoT middleware (EdgeX, AWS Greengrass) assumes enterprise scale. InterLink is for teams that need protocol translation in under an hour with zero infrastructure overhead.

---

# Slide 2 — The Problem

- IoT devices speak different protocols: MQTT, HTTP, CoAP, Modbus
- Vendor SDKs create data silos with no interoperability
- Cloud-first platforms add latency and single points of failure
- Edge gateways today are either too simple or too complex
- No lightweight standard for canonical device message exchange

NOTE: A factory floor might have Modbus PLCs, MQTT sensors, and HTTP cameras — today each needs its own pipeline. InterLink eliminates that with one internal contract.

---

# Slide 3 — The Solution

- Single canonical Protobuf message for all device data
- Protocol adapters translate native formats at the boundary
- NATS bus routes messages — adapters never call each other
- SQLite registry with heartbeat, persistent cache, and dead-letters
- Policy engine with hot-reload and device-to-device command routing
- `/health` and `/metrics` endpoints for operational visibility

NOTE: The key insight is that protocol translation is a solved problem per-protocol. The unsolved problem is the internal contract. InterLink solves that with one Protobuf schema and a three-segment NATS subject: iot.<type>.<device_id>.

---

# Slide 4 — Architecture Overview

- **Adapter Layer** — protocol-specific ingestion (MQTT, HTTP, CoAP)
- **Canonical Model** — Protobuf Message with typed payloads
- **NATS Bus** — publish/subscribe backbone with optional JetStream persistence
- **Policy Engine** — rule-based allow/deny + hot-reload + command routing
- **Device Registry** — SQLite store with heartbeat, latest-message cache, dead-letters
- **Gateway** — wires all layers, config validation, heartbeat goroutine, graceful shutdown

NOTE: These six layers are fixed. Adding a new protocol only touches the Adapter Layer — everything below it stays untouched.

---

# Slide 5 — Canonical Data Model

- `Message` wraps identity, timestamp, source, and oneof payload
- Three payload types: `TelemetryPayload`, `CommandPayload`, `EventPayload`
- `metadata` map carries adapter-specific context without schema changes
- `source_proto` is a string, not enum — no rebuild for new adapters
- NATS subject derived from payload type: `iot.telemetry.<device_id>`

NOTE: We chose oneof over google.protobuf.Any for compile-time safety. Every consumer knows the exact set of payload shapes. Field numbers 5–9 are intentionally reserved for future top-level fields without breaking wire compatibility.

---

# Slide 6 — MQTT → HTTP Data Flow

- MQTT device publishes JSON to `devices/sensor-42`
- MQTT adapter validates topic structure, extracts device_id
- Flat or nested JSON → `TelemetryPayload`
- Adapter publishes to NATS subject `iot.telemetry.sensor-42`
- HTTP consumer API serves it via `GET /api/v1/devices/sensor-42/latest`
- Latest message persisted in SQLite — survives restarts

NOTE: Walk through this live: run mosquitto_pub in one terminal, curl the HTTP latest endpoint in another. Nested JSON like `{"readings":{"temperature":23.5}}` also works.

---

# Slide 7 — Adapter Interface

- Three methods: `Name()`, `Start(ctx, bus, registry)`, `Stop(ctx)`
- Adapters receive shared bus and registry — no self-wiring
- Structured logging via `log/slog` with `"component"` field
- Adding Modbus requires one file: `internal/adapter/modbus/modbus.go`
- Gateway starts all adapters uniformly — no adapter-specific logic

NOTE: The ADAPTER.md doc walks through creating a new adapter step-by-step. Emphasize that the interface is intentionally small — three methods is all you implement.

---

# Slide 8 — Reliability & Operations

- **Heartbeat timeout** — devices not seen in 5m automatically marked inactive
- **Publish retry** — 3 retries with exponential backoff, then dead-letter table
- **NATS reconnection** — auto-reconnect with structured logging
- **JetStream** — optional durable subscriptions for offline message replay
- **Config validation** — all required fields validated at startup
- **`/health`** — uptime, NATS status, device count, adapter names
- **`/metrics`** — Prometheus-compatible metrics endpoint

NOTE: These operational features make InterLink production-ready. The health and metrics endpoints integrate with standard monitoring stacks.

---

# Slide 9 — What This Is Not + Strategic Position

- Not a platform — no UI, no dashboards, no cloud console
- Not EdgeX — no microservices, no Docker-compose, no 50+ repos
- Not a device manager — no OTA, no firmware, no provisioning
- Target: small teams needing protocol bridge in hours, not weeks
- Runs on Raspberry Pi, edge gateways, and constrained VMs

NOTE: The strategic gap is between "write your own MQTT-to-HTTP script" and "deploy EdgeX Foundry." InterLink fills that gap.

---

# Slide 10 — Implemented Features + Vision

- ✅ Canonical model, bus, adapters (MQTT/HTTP/CoAP), policy, gateway
- ✅ JetStream persistence, heartbeat timeout, publish retry
- ✅ Hot-reload policy, command routing, structured logging
- ✅ Health endpoint, Prometheus metrics, config validation
- Next: Modbus adapter, WebSocket consumer, CoAP DTLS, TLS/mTLS
- Open-core potential: commercial policy UI, fleet registry, audit log
- Future state: **any protocol in, any protocol out, one edge binary**

NOTE: The open-core angle is real — the policy engine and registry are natural extension points for a commercial layer. The core stays MIT-licensed and minimal.
