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

The table below is based on recorded load test runs in this repository (`comp.txt`) at equal message counts and rates.

| Scenario | System | Throughput (msg/s) | p50 (ms) | p95 (ms) | p99 (ms) | Errors |
|---|---|---:|---:|---:|---:|---:|
| 1000 msgs @ 100/s | InterLink | 98.48 | 0.346 | 1.075 | 1.603 | 0 |
| 1000 msgs @ 100/s | Node-RED | 98.53 | 0.287 | 0.762 | 0.979 | 0 |
| 5000 msgs @ 500/s | InterLink | 465.38 | 0.209 | 0.985 | 1.729 | 0 |

Interpretation:
- At moderate load (100/s), both systems sustain the target rate with zero errors.
- At higher load (500/s), InterLink remains stable with sub-2ms p99 publish latency and zero drops in the recorded run.
- InterLink health snapshots during load show low memory footprint and consistent goroutine counts, which supports edge deployment goals.
- The current repository data includes one 5000@500 Node-RED run transcript without a complete JSON block in the captured snippet, so that row is intentionally not extrapolated.

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
