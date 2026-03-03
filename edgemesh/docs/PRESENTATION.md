# Slide 1 — EdgeMesh: Lightweight IoT Interoperability Middleware

- Bridges IoT protocols through a canonical Protobuf model
- Runs at the edge — no cloud dependency required
- NATS message bus decouples all protocol adapters
- Sub-1000 LOC Go codebase, single binary

NOTE: EdgeMesh exists because most IoT middleware (EdgeX, AWS Greengrass) assumes enterprise scale. EdgeMesh is for teams that need protocol translation in under an hour with zero infrastructure overhead.

---

# Slide 2 — The Problem

- IoT devices speak different protocols: MQTT, HTTP, CoAP, Modbus
- Vendor SDKs create data silos with no interoperability
- Cloud-first platforms add latency and single points of failure
- Edge gateways today are either too simple or too complex
- No lightweight standard for canonical device message exchange

NOTE: A factory floor might have Modbus PLCs, MQTT sensors, and HTTP cameras — today each needs its own pipeline. EdgeMesh eliminates that with one internal contract.

---

# Slide 3 — The Solution

- Single canonical Protobuf message for all device data
- Protocol adapters translate native formats at the boundary
- NATS bus routes messages — adapters never call each other
- SQLite registry tracks devices with zero external dependencies
- Policy engine filters messages before forwarding

NOTE: The key insight is that protocol translation is a solved problem per-protocol. The unsolved problem is the internal contract. EdgeMesh solves that with one Protobuf schema and a three-segment NATS subject: iot.<type>.<device_id>.

---

# Slide 4 — Architecture Overview

- **Adapter Layer** — protocol-specific ingestion (MQTT, HTTP)
- **Canonical Model** — Protobuf Message with typed payloads
- **NATS Bus** — publish/subscribe backbone, subject-based routing
- **Policy Engine** — rule-based allow/deny before forwarding
- **Device Registry** — SQLite store for known devices
- **Gateway** — wires all layers, handles lifecycle and signals

NOTE: These six layers are fixed. Adding a new protocol only touches the Adapter Layer — everything below it stays untouched. This is the core architectural guarantee.

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
- MQTT adapter extracts device_id from topic segment index
- Flat JSON `{"temperature": 23.5}` → `TelemetryPayload`
- Adapter publishes to NATS subject `iot.telemetry.sensor-42`
- HTTP consumer API serves it via `GET /api/v1/devices/sensor-42/latest`

NOTE: Walk through this live: run mosquitto_pub in one terminal, curl the HTTP latest endpoint in another. The audience sees a message cross protocol boundaries in real time with zero glue code.

---

# Slide 7 — Adapter Interface

- Three methods: `Name()`, `Start(ctx, bus, registry)`, `Stop(ctx)`
- Adapters receive shared bus and registry — no self-wiring
- `ConverterFunc` type handles protocol-specific byte transformation
- Adding CoAP requires one file: `internal/adapter/coap/coap.go`
- Gateway starts all adapters uniformly — no adapter-specific logic

NOTE: The ADAPTER.md doc walks through creating a CoAP adapter step-by-step. Emphasize that the interface is intentionally small — three methods is all you implement. The gateway does the rest.

---

# Slide 8 — Developer Experience

- `go build ./cmd/gateway && ./edgemesh-gateway` — single binary
- Config is one YAML file with inline documentation
- HTTP API supports ingest, latest, SSE streaming, and commands
- Test with `curl` and `mosquitto_pub` — no SDK required
- `go vet ./...` passes clean — no warnings, no hacks

NOTE: Demo sequence: start NATS and Mosquitto, run the binary, then show the four curl commands from ADAPTERS_MQTT_HTTP.md. The SSE stream endpoint is the crowd-pleaser — open it in one terminal and publish MQTT in another.

---

# Slide 9 — What This Is Not + Strategic Position

- Not a platform — no UI, no dashboards, no cloud console
- Not EdgeX — no microservices, no Docker-compose, no 50+ repos
- Not a device manager — no OTA, no firmware, no provisioning
- Target: small teams needing protocol bridge in hours, not weeks
- Runs on Raspberry Pi, edge gateways, and constrained VMs

NOTE: The strategic gap is between "write your own MQTT-to-HTTP script" and "deploy EdgeX Foundry." EdgeMesh fills that gap. It's for the team that needs interoperability today without hiring a platform team.

---

# Slide 10 — Roadmap + Vision

- Phase 1–4 complete: canonical model, bus, adapters, policy, gateway
- Next: CoAP and Modbus adapters, JetStream persistence
- Planned: WebSocket consumer adapter, Prometheus metrics export
- Open-core potential: commercial policy UI, fleet registry, audit log
- Future state: **any protocol in, any protocol out, one edge binary**

NOTE: The open-core angle is real — the policy engine and registry are natural extension points for a commercial layer. The core stays MIT-licensed and minimal. Emphasize that the architecture was designed for this split from day one.
