# InterLink

InterLink is a lightweight IoT interoperability gateway that normalizes MQTT, HTTP, and CoAP traffic into a canonical Protobuf message model and routes it over NATS (or JetStream). It is designed for edge deployments where you want a single binary, low memory overhead, and clear extension points.

## What It Does

- Bridges MQTT, HTTP, and CoAP producers into one canonical event format.
- Uses NATS subjects (`iot.<type>.<device_id>`) as the internal message bus contract.
- Applies hot-reloadable policy rules (allow/deny and optional command routing).
- Persists device registry state and latest messages in SQLite.
- Exposes operational APIs: ingest, latest message lookup, SSE stream, health, and Prometheus metrics.
- Supports declarative pipelines (source -> transforms -> sink), including script transforms.
- Ships with a read-only web UI for pipeline and device visibility.

## Quick Start

### Prerequisites

- Go 1.24+
- NATS server
- MQTT broker (for MQTT ingestion)
- Optional: CoAP client (`coap-client`) for CoAP validation

### Build and Run

```bash
go mod tidy
go build -o interlink-gateway ./cmd/gateway
./interlink-gateway -config config/config.yaml
```

### Required Services

```bash
nats-server
mosquitto
```

### Smoke Test

```bash
# MQTT publish
mosquitto_pub -t "devices/sensor-42" -m '{"temperature":23.5}'

# HTTP ingest
curl -X POST http://localhost:8080/ingest/v1/pump-01/telemetry \
  -H "Content-Type: application/json" \
  -d '{"metric":"pressure","value":4.2,"unit":"bar"}'

# Read latest message
curl http://localhost:8080/api/v1/devices/sensor-42/latest

# Health and metrics
curl http://localhost:8080/health
curl http://localhost:8080/metrics
```

## Pipeline DSL

Pipelines are configured in `config/config.yaml` under `pipelines:`.

```yaml
pipelines:
  - name: temperature-bridge-http
    source:
      type: nats
      subject: iot.telemetry.>
    transforms:
      - type: extract
        field: temperature
        as: value
      - type: scale
        factor: 1.8
        offset: 32
        as: temp_f
      - type: rename
        from: temp_f
        to: value
      - type: filter
        field: value
        min: -100
        max: 250
      - type: script
        script: |
          value = value * 1.8 + 32
          if value > 200 { value = 200 }
    sink:
      type: http_post
      url: http://dashboard.local/ingest
      timeout: 3s
```

Supported transforms:
- `extract`
- `scale`
- `rename`
- `filter`
- `script` (Tengo)

Supported sinks:
- `http_post`
- `nats_publish`
- `mqtt_publish`

## Architecture

```text
MQTT / HTTP / CoAP Adapters
            |
            v
      Canonical Protobuf
            |
            v
      NATS / JetStream Bus
            |
            +--> Policy Engine (allow/deny, command route)
            +--> Pipeline Runtime (DSL transforms and sinks)
            +--> Registry Writer (SQLite latest + metadata)
            |
            v
     HTTP API + SSE + Prometheus + UI
```

Key subject convention:
- `iot.telemetry.<device_id>`
- `iot.command.<device_id>`
- `iot.event.<device_id>`

## Performance Snapshot (InterLink vs Node-RED)

The table below is based on recorded runs in `comp.txt` with identical message counts and producer settings.

| Test Scenario | System | Messages | Target Rate (msg/s) | Actual Throughput (msg/s) | Duration (s) | Latency Min (ms) | Latency Max (ms) |
|---|---|---:|---:|---:|---:|---:|---:|
| Controlled load | InterLink | 1000 | 100 | 98.48 | 10.15 | 0.067 | 2.778 |
| Controlled load | Node-RED | 1000 | 100 | 98.53 | 10.15 | 0.106 | 1.682 |
| Medium load | InterLink | 5000 | 500 | 465.38 | 10.74 | 0.042 | 3.637 |
| Medium load | Node-RED | 5000 | 500 | 465.78 | 10.73 | 0.048 | 4.165 |
| Unlimited rate (ceiling) | InterLink | 5000 | 0 (unlimited) | 24497.18 | 0.20 | 0.023 | 5.301 |
| Unlimited rate (ceiling) | Node-RED | 5000 | 0 (unlimited) | 26339.34 | 0.19 | 0.024 | 4.282 |

### Derived Delta (InterLink - Node-RED)

| Scenario | Throughput Delta (msg/s) | Throughput Delta (%) | Duration Delta (s) | Min Latency Delta (ms) | Max Latency Delta (ms) |
|---|---:|---:|---:|---:|---:|
| 1000 @ 100/s | -0.05 | -0.05% | 0.00 | -0.039 | +1.096 |
| 5000 @ 500/s | -0.40 | -0.09% | +0.01 | -0.006 | -0.528 |
| 5000 @ unlimited | -1842.16 | -6.99% | +0.01 | -0.001 | +1.019 |

Negative latency delta means InterLink has lower latency for that metric.

## Node-RED Comparison

Numerical deployment and footprint comparison from this local environment:

| Field | InterLink | Node-RED | Delta |
|---|---:|---:|---:|
| Deployable artifact size (bytes) | 19802112 | 79790181 | InterLink is 59988069 bytes smaller |
| Size ratio | 1.00x | 4.03x | Node-RED package is 4.03x larger |
| Runtime dependency count | 0 external runtimes | 1 required runtime (Node.js v24.14.0) | InterLink avoids runtime dependency |

## Extending Adapters

To add a new protocol adapter, implement the adapter interface and register it in gateway wiring.

Detailed guide:
- [docs/ADAPTER.md](docs/ADAPTER.md)

Related docs:
- [docs/ADAPTERS_MQTT_HTTP.md](docs/ADAPTERS_MQTT_HTTP.md)
- [docs/COAP.md](docs/COAP.md)
- [docs/BUS.md](docs/BUS.md)
- [docs/POLICY.md](docs/POLICY.md)
- [docs/REGISTRY.md](docs/REGISTRY.md)
- [docs/BENCHMARK.md](docs/BENCHMARK.md)

## UI

Default UI endpoint:
- `http://localhost:8081`

UI APIs:
- `GET /api/ui/pipelines`
- `GET /api/ui/devices`

## Build Targets

```bash
go test ./... -race -count=1
go build ./...
go build -trimpath -ldflags="-s -w" -o interlink-gateway ./cmd/gateway
```
