# MQTT & HTTP Adapters

## MQTT Flow

```
MQTT Broker                       InterLink                         NATS
    │                                │                               │
    │  publish devices/sensor-42     │                               │
    │  {"temperature": 23.5}         │                               │
    │ ──────────────────────────────►│                               │
    │                                │ validate topic structure      │
    │                                │ extract device_id from topic  │
    │                                │ topic[1] = "sensor-42"        │
    │                                │                               │
    │                                │ convert JSON → TelemetryPayload
    │                                │   (flat or nested JSON)       │
    │                                │   metric="temperature"        │
    │                                │   value=23.5                  │
    │                                │                               │
    │                                │ publish iot.telemetry.sensor-42
    │                                │ ─────────────────────────────►│
    │                                │                               │
    │                                │ registry.Register(sensor-42)  │
    │                                │                               │
```

1. The MQTT adapter connects to the broker specified in `config.yaml` (`mqtt.broker`).
2. On connect (and reconnect), it subscribes to `mqtt.topic` (e.g. `devices/#`).
3. **Topic validation (Fix 9):** When a message arrives, the adapter validates the topic structure — if the topic has fewer segments than `device_id_topic_index`, a structured error is logged and the message is dropped.
4. The device ID is extracted from the topic by splitting on `/` and picking the segment at `mqtt.device_id_topic_index`.
5. **Nested JSON (Fix 2):** The JSON payload can be flat (`{"temperature": 23.5}`) or nested one level (`{"readings": {"temperature": 23.5}}`). The adapter first checks top-level numeric values, then walks one level deeper. If no numeric value is found, a descriptive error is returned. If the payload is not valid JSON at all, a descriptive error with the raw payload is returned.
6. The adapter marshals a canonical `Message` and publishes to `iot.telemetry.<device_id>`.
7. The device is upserted into the registry with protocol `mqtt` and status `active`.

## HTTP Ingest Flow

```
Client                            InterLink                         NATS
  │                                  │                               │
  │ POST /ingest/v1/pump-01/telemetry                                │
  │ {"metric":"pressure","value":4.2,"unit":"bar"}                   │
  │ ────────────────────────────────►│                               │
  │                                  │ build TelemetryPayload        │
  │                                  │ publish iot.telemetry.pump-01 │
  │                                  │ ─────────────────────────────►│
  │                                  │                               │
  │  202 {"message_id":"a1b2c3..."}  │                               │
  │ ◄────────────────────────────────│                               │
```

The ingest endpoint accepts a JSON body with `metric`, `value`, and `unit`. The `device_id` comes from the URL path.

## HTTP Consumer API

### GET /api/v1/devices/{device_id}/latest

Returns the most recently received telemetry message for the device as JSON. **The latest message is persisted in SQLite (Fix 3)** — it survives gateway restarts.

Response:
```json
{
  "message_id": "a1b2c3d4e5f6...",
  "device_id": "sensor-42",
  "timestamp_ms": 1709356800000,
  "source_proto": "mqtt",
  "type": "telemetry",
  "payload": {
    "metric": "temperature",
    "value": 23.5,
    "unit": ""
  }
}
```

### GET /api/v1/devices/{device_id}/stream

Server-Sent Events (SSE) endpoint. Pushes every new telemetry message for the device in real time.

**SSE channel capacity (Fix 7):** Configurable via `http.sse_channel_capacity` (default 256). When the channel is full, the oldest message is dropped and a structured warning is logged — the consumer always receives the most recent data.

Each SSE event:
```
data: {"message_id":"...","device_id":"sensor-42","timestamp_ms":1709356800000,...}
```

### POST /api/v1/devices/{device_id}/command

Publishes a command to `iot.command.<device_id>` using **NATS request/reply (Fix 6)** with a configurable timeout (`http.command_timeout`, default 5s). Returns acknowledgment status in the response.

Request body:
```json
{
  "action": "reboot",
  "params": {"delay_sec": 5}
}
```

Response (acknowledged):
```json
{"message_id":"...","ack_status":"ok","ack_data":{...}}
```

Response (timeout):
```json
{"message_id":"...","ack_status":"timeout","error":"nats: timeout"}
```

### GET /health (Enhanced)

Returns comprehensive gateway health and performance status:
```json
{
  "uptime_seconds": 3600,
  "nats_connected": true,
  "device_count": 42,
  "adapters": ["mqtt", "http", "coap"],
  "memory": {
    "heap_alloc_mb": 12.5,
    "heap_inuse_mb": 16.0,
    "stack_inuse_mb": 0.8,
    "sys_mb": 24.3,
    "gc_pause_ms": 0.45,
    "gc_runs": 128
  },
  "runtime": {
    "goroutines": 23,
    "go_version": "go1.24.1"
  },
  "storage": {
    "db_size_mb": 1.2,
    "dead_letters": 0,
    "latest_messages": 15
  },
  "throughput": {
    "mqtt_received": 15420,
    "mqtt_published": 15420,
    "http_received": 3200,
    "http_published": 3200,
    "coap_received": 890,
    "coap_published": 890,
    "total_published": 19510
  }
}
```

### GET /metrics (Prometheus)

Prometheus-compatible metrics endpoint. Exports ~20 metrics across 6 categories:

| Category | Metric | Type | Labels |
|---|---|---|---|
| Throughput | `interlink_messages_published_total` | Counter | `adapter` |
| Throughput | `interlink_messages_received_total` | Counter | `adapter` |
| Latency | `interlink_message_processing_seconds` | Histogram | `adapter`, `stage` |
| Latency | `interlink_nats_publish_seconds` | Histogram | — |
| Latency | `interlink_http_request_seconds` | Histogram | `method`, `path` |
| Latency | `interlink_policy_evaluation_seconds` | Histogram | — |
| Policy | `interlink_policy_decisions_total` | Counter | `action` |
| SSE | `interlink_sse_clients_active` | Gauge | — |
| Registry | `interlink_registry_devices` | Gauge | `protocol`, `status` |
| Storage | `interlink_sqlite_db_size_bytes` | Gauge | — |
| Storage | `interlink_dead_letters_total` | Gauge | — |
| Memory | `interlink_go_heap_alloc_bytes` | Gauge | — |
| Memory | `interlink_go_heap_inuse_bytes` | Gauge | — |
| Memory | `interlink_go_stack_inuse_bytes` | Gauge | — |
| Memory | `interlink_go_sys_bytes` | Gauge | — |
| GC | `interlink_go_gc_pause_seconds` | Histogram | — |
| GC | `interlink_go_gc_runs_total` | Counter | — |
| Runtime | `interlink_go_goroutines` | Gauge | — |
| Runtime | `interlink_uptime_seconds` | Gauge | — |
| NATS | `interlink_nats_reconnections_total` | Counter | — |

The `stage` label for `interlink_message_processing_seconds` can be: `convert`, `unmarshal`, `marshal`, `publish`, or `total`.

## Configuration Reference

```yaml
nats:
  url: "nats://127.0.0.1:4222"          # NATS server URL
  jetstream: false                        # Enable JetStream for durable subs

mqtt:
  broker: "tcp://127.0.0.1:1883"        # MQTT broker address
  client_id: "interlink-gw-01"           # Unique client ID
  topic: "devices/#"                    # Subscription topic filter
  qos: 1                               # 0|1|2
  device_id_topic_index: 1             # Index in topic split by '/'

http:
  listen: ":8080"                       # HTTP listen address
  sse_channel_capacity: 256             # SSE channel buffer per client
  command_timeout: "5s"                 # Command ack timeout

registry:
  db_path: "./interlink.db"              # SQLite file path

heartbeat_timeout: "5m"                  # Devices not seen → inactive
```

### curl Commands

**Ingest telemetry via HTTP:**
```bash
curl -X POST http://localhost:8080/ingest/v1/sensor-42/telemetry \
  -H "Content-Type: application/json" \
  -d '{"metric":"temperature","value":23.5,"unit":"celsius"}'
```

**Get latest message (persistent):**
```bash
curl http://localhost:8080/api/v1/devices/sensor-42/latest
```

**Stream messages (SSE):**
```bash
curl -N http://localhost:8080/api/v1/devices/sensor-42/stream
```

**Send command (with acknowledgment):**
```bash
curl -X POST http://localhost:8080/api/v1/devices/sensor-42/command \
  -H "Content-Type: application/json" \
  -d '{"action":"reboot","params":{"delay_sec":5}}'
```

**Health check:**
```bash
curl http://localhost:8080/health
```

**Prometheus metrics:**
```bash
curl http://localhost:8080/metrics
```
