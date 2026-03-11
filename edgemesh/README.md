# EdgeMesh

A lightweight IoT interoperability middleware that bridges MQTT, HTTP, and CoAP devices through a canonical Protobuf model over a NATS message bus. Designed for edge deployments where simplicity, low resource usage, and protocol-agnostic routing matter.

## Architecture

```
 ┌──────────┐                                              ┌──────────────┐
 │  MQTT    │         ┌──────────────────────────┐         │   HTTP API   │
 │  Broker  │         │        NATS Bus          │         │  /health     │
 └────┬─────┘         │  (or JetStream)          │         │  /metrics    │
      │               │                          │         └───┬──────┬───┘
      ▼               │  iot.telemetry.<device>  │             │      │
 ┌─────────┐ publish  │  iot.command.<device>    │    ingest   │      │ consume
 │  MQTT   │─────────►│  iot.event.<device>      │◄────────────┘      │
 │ Adapter │          │                          │────────────────────┘
 └─────────┘          │     Policy Engine        │
                      │  (hot-reload, cmd route) │
 ┌─────────┐ publish  │                          │
 │  CoAP   │─────────►│                          │
 │ Adapter │  (UDP)   │                          │
 └─────────┘          └──────────┬───────────────┘
                                 │
                      ┌──────────▼───────────────┐
                      │     SQLite Registry      │
                      │  devices | latest_msgs   │
                      │  dead_letters            │
                      └──────────────────────────┘
```

## Features

- **Protocol bridging** — MQTT, HTTP, CoAP adapters with a canonical Protobuf model
- **Heartbeat timeout** — devices not seen within a configurable duration (default 5m) are automatically marked inactive
- **NATS JetStream** — opt-in durable subscriptions with message replay for offline subscribers
- **Publish retry + dead-letter** — 3 retries with exponential backoff; failures stored in SQLite
- **Policy engine** — rule-based allow/deny with hot-reload via `fsnotify` and device-to-device command routing
- **Command acknowledgment** — NATS request/reply with configurable timeout (default 5s)
- **Persistent latest-message cache** — survives gateway restarts (SQLite-backed)
- **Health endpoint** — `GET /health` returns uptime, NATS status, device count, adapter names
- **Prometheus metrics** — `GET /metrics` with publish counts, policy decisions, SSE clients, device count
- **Structured logging** — all logs via `log/slog` with JSON output, structured fields (`component`, `device_id`, `error`)
- **Pure-Go build** — uses `modernc.org/sqlite` (no CGo) for easy cross-compilation
- **Config validation** — all required fields validated at startup with clear error messages

## Quick Start

```bash
# Prerequisites: Go 1.22+, NATS server, MQTT broker (e.g. Mosquitto)
# Optional: coap-client (libcoap) for CoAP testing

# 1. Clone and build (no C toolchain needed — pure Go)
git clone <repo-url> && cd edgemesh
go mod tidy
go build -o edgemesh-gateway ./cmd/gateway

# 2. Start infrastructure
nats-server &                    # default port 4222
mosquitto &                      # default port 1883

# 3. Run EdgeMesh
./edgemesh-gateway -config config/config.yaml
```

## Demo Walkthrough

Open three terminals:

**Terminal 1 — Start EdgeMesh:**
```bash
./edgemesh-gateway -config config/config.yaml
```

**Terminal 2 — Send telemetry via MQTT and HTTP:**
```bash
# MQTT: publish temperature from sensor-42 (flat or nested JSON supported)
mosquitto_pub -t "devices/sensor-42" -m '{"temperature": 23.5}'
mosquitto_pub -t "devices/sensor-42" -m '{"readings": {"temperature": 23.5}}'

# HTTP: ingest pressure from pump-01
curl -X POST http://localhost:8080/ingest/v1/pump-01/telemetry \
  -H "Content-Type: application/json" \
  -d '{"metric":"pressure","value":4.2,"unit":"bar"}'
```

**Terminal 3 — Send telemetry via CoAP (UDP):**
```bash
# CoAP: publish humidity from sensor-99
coap-client -m post coap://localhost:5683/telemetry/sensor-99 \
  -e '{"metric":"humidity","value":62.1,"unit":"%"}'
```

**Terminal 4 — Consume via HTTP API:**
```bash
# Get latest message for sensor-42 (persisted in SQLite, survives restarts)
curl http://localhost:8080/api/v1/devices/sensor-42/latest

# Stream real-time messages for pump-01
curl -N http://localhost:8080/api/v1/devices/pump-01/stream

# Send a command with acknowledgment (5s timeout)
curl -X POST http://localhost:8080/api/v1/devices/sensor-42/command \
  -H "Content-Type: application/json" \
  -d '{"action":"reboot","params":{"delay_sec":5}}'

# Check gateway health
curl http://localhost:8080/health

# Prometheus metrics
curl http://localhost:8080/metrics
```

## Adding a New Protocol

See [docs/ADAPTER.md](docs/ADAPTER.md) for a complete walkthrough using a fictional adapter as a concrete example.

## Configuration Reference

See [config/config.yaml](config/config.yaml) — every field has inline comments.

## Project Structure

```
edgemesh/
├── cmd/gateway/main.go              # Entrypoint — slog init, parses flags, calls gateway.Run()
├── internal/
│   ├── adapter/
│   │   ├── adapter.go               # Adapter interface + ConverterFunc type
│   │   ├── mqtt/mqtt.go             # MQTT inbound adapter (nested JSON, topic validation)
│   │   ├── http/http.go             # HTTP ingest + consumer API + /health + /metrics
│   │   └── coap/coap.go             # CoAP inbound adapter (UDP, device ID validation)
│   ├── bus/
│   │   ├── bus.go                   # MessageBus interface + NATSBus (request/reply, retry)
│   │   └── jetstream.go             # JetStreamBus (durable subscriptions, message replay)
│   ├── canonical/
│   │   ├── canonical.pb.go          # Generated Protobuf types
│   │   └── canonical.go             # Message constructors + helpers
│   ├── registry/registry.go         # SQLite registry (devices, latest_messages, dead_letters)
│   ├── policy/policy.go             # Policy engine (hot-reload, command routing, counters)
│   └── gateway/gateway.go           # Wires bus, registry, policy, adapters, heartbeat, config validation
├── proto/canonical.proto             # Canonical message schema
├── config/config.yaml                # Runtime configuration
├── Makefile                          # build, run, proto, clean
└── docs/
    ├── CANONICAL.md                  # Schema decisions
    ├── BUS.md                        # NATS bus + JetStream documentation
    ├── ADAPTER.md                    # How to add a new adapter
    ├── REGISTRY.md                   # Device registry documentation
    ├── POLICY.md                     # Policy engine documentation
    ├── ADAPTERS_MQTT_HTTP.md         # MQTT + HTTP adapter details
    ├── COAP.md                       # CoAP adapter details
    └── PRESENTATION.md              # Presentation slides
```
