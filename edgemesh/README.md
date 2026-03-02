# EdgeMesh

A lightweight IoT interoperability middleware that bridges MQTT and HTTP devices through a canonical Protobuf model over a NATS message bus. Designed for edge deployments where simplicity, low resource usage, and protocol-agnostic routing matter.

## Architecture

```
 ┌──────────┐                                              ┌──────────────┐
 │  MQTT    │         ┌──────────────────────────┐         │   HTTP API   │
 │  Broker  │         │        NATS Bus          │         │              │
 └────┬─────┘         │                          │         └───┬──────┬───┘
      │               │  iot.telemetry.<device>  │             │      │
      ▼               │  iot.command.<device>    │    ingest   │      │ consume
 ┌─────────┐ publish  │  iot.event.<device>      │◄────────────┘      │
 │  MQTT   │─────────►│                          │────────────────────┘
 │ Adapter │          │     Policy Engine         │
 └─────────┘          │     (interceptor)        │
                      └──────────┬───────────────┘
                                 │
                      ┌──────────▼───────────────┐
                      │     SQLite Registry      │
                      │  device_id | protocol    │
                      └──────────────────────────┘
```

## Quick Start

```bash
# Prerequisites: Go 1.22+, NATS server, MQTT broker (e.g. Mosquitto)

# 1. Clone and build
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
# MQTT: publish temperature from sensor-42
mosquitto_pub -t "devices/sensor-42" -m '{"temperature": 23.5}'

# HTTP: ingest pressure from pump-01
curl -X POST http://localhost:8080/ingest/v1/pump-01/telemetry \
  -H "Content-Type: application/json" \
  -d '{"metric":"pressure","value":4.2,"unit":"bar"}'
```

**Terminal 3 — Consume via HTTP API:**
```bash
# Get latest message for sensor-42 (arrived via MQTT)
curl http://localhost:8080/api/v1/devices/sensor-42/latest

# Stream real-time messages for pump-01
curl -N http://localhost:8080/api/v1/devices/pump-01/stream

# Send a command to sensor-42
curl -X POST http://localhost:8080/api/v1/devices/sensor-42/command \
  -H "Content-Type: application/json" \
  -d '{"action":"reboot","params":{"delay_sec":5}}'
```

## Adding a New Protocol

See [docs/ADAPTER.md](docs/ADAPTER.md) for a complete walkthrough using a fictional CoAP adapter as a concrete example.

## Configuration Reference

See [config/config.yaml](config/config.yaml) — every field has inline comments.

## Project Structure

```
edgemesh/
├── cmd/gateway/main.go              # Entrypoint — parses flags, calls gateway.Run()
├── internal/
│   ├── adapter/
│   │   ├── adapter.go               # Adapter interface + ConverterFunc type
│   │   ├── mqtt/mqtt.go             # MQTT inbound adapter (Paho client)
│   │   └── http/http.go             # HTTP ingest + consumer API adapter
│   ├── bus/bus.go                    # MessageBus interface + NATS implementation
│   ├── canonical/
│   │   ├── canonical.pb.go          # Generated Protobuf types
│   │   └── canonical.go             # Message constructors + helpers
│   ├── registry/registry.go         # SQLite device registry
│   ├── policy/policy.go             # Rule-based policy engine
│   └── gateway/gateway.go           # Wires bus, registry, policy, adapters
├── proto/canonical.proto             # Canonical message schema
├── config/config.yaml                # Runtime configuration
├── Makefile                          # build, run, proto, clean
└── docs/
    ├── CANONICAL.md                  # Schema decisions
    ├── BUS.md                        # NATS bus documentation
    ├── ADAPTER.md                    # How to add a new adapter
    ├── REGISTRY.md                   # Device registry documentation
    ├── POLICY.md                     # Policy engine documentation
    └── ADAPTERS_MQTT_HTTP.md         # MQTT + HTTP adapter details
```
