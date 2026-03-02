# MQTT & HTTP Adapters

## MQTT Flow

```
MQTT Broker                       EdgeMesh                         NATS
    │                                │                               │
    │  publish devices/sensor-42     │                               │
    │  {"temperature": 23.5}         │                               │
    │ ──────────────────────────────►│                               │
    │                                │ extract device_id from topic  │
    │                                │ topic[1] = "sensor-42"        │
    │                                │                               │
    │                                │ convert JSON → TelemetryPayload
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
3. When a message arrives on topic `devices/sensor-42`, it splits the topic by `/` and picks the segment at `mqtt.device_id_topic_index` (default `1`) as the device ID.
4. The JSON payload must be a flat object with at least one numeric value. The first numeric key becomes the `TelemetryPayload` metric.
5. The adapter marshals a canonical `Message` and publishes to `iot.telemetry.<device_id>`.
6. The device is upserted into the registry with protocol `mqtt` and status `active`.

## HTTP Ingest Flow

```
Client                            EdgeMesh                         NATS
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

Returns the most recently received telemetry message for the device as JSON.

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

Each SSE event:
```
data: {"message_id":"...","device_id":"sensor-42","timestamp_ms":1709356800000,...}
```

### POST /api/v1/devices/{device_id}/command

Publishes a command to `iot.command.<device_id>`.

Request body:
```json
{
  "action": "reboot",
  "params": {"delay_sec": 5}
}
```

## Message Routing Diagram

```
 ┌──────────┐         ┌────────────────────────────────────┐         ┌──────────────┐
 │  MQTT    │         │              NATS BUS              │         │   HTTP API   │
 │  Broker  │         │                                    │         │              │
 └────┬─────┘         │  iot.telemetry.<device_id>         │         └──────┬───────┘
      │               │  iot.command.<device_id>           │                │
      │  subscribe    │  iot.event.<device_id>             │   POST /ingest│
      ▼               │                                    │◄───────────────┘
 ┌─────────┐ publish  │                                    │  publish
 │  MQTT   │─────────►│                                    │◄──────────┐
 │ Adapter │          │                                    │           │
 └─────────┘          │                                    │    ┌──────┴──────┐
                      │                                    │───►│ HTTP Adapter │
                      │                   subscribe        │    │  (consumer)  │
                      │              iot.telemetry.>       │    │             │
                      └────────────────────────────────────┘    │ GET /latest │
                                                                │ GET /stream │
                      ┌────────────────┐                        │ POST /cmd   │
                      │   SQLite       │◄───register────────────┴─────────────┘
                      │   Registry     │◄───register──── MQTT Adapter
                      └────────────────┘
```

## Configuration Reference

```yaml
nats:
  url: "nats://127.0.0.1:4222"          # NATS server URL

mqtt:
  broker: "tcp://127.0.0.1:1883"        # MQTT broker address
  client_id: "edgemesh-gw-01"           # Unique client ID
  topic: "devices/#"                    # Subscription topic filter
  qos: 1                               # 0|1|2
  device_id_topic_index: 1             # Index in topic split by '/'
                                        # devices/sensor-42 → index 1 = "sensor-42"

http:
  listen: ":8080"                       # HTTP listen address

registry:
  db_path: "./edgemesh.db"              # SQLite file path
```

### curl Commands

**Ingest telemetry via HTTP:**
```bash
curl -X POST http://localhost:8080/ingest/v1/sensor-42/telemetry \
  -H "Content-Type: application/json" \
  -d '{"metric":"temperature","value":23.5,"unit":"celsius"}'
```

**Get latest message:**
```bash
curl http://localhost:8080/api/v1/devices/sensor-42/latest
```

**Stream messages (SSE):**
```bash
curl -N http://localhost:8080/api/v1/devices/sensor-42/stream
```

**Send command:**
```bash
curl -X POST http://localhost:8080/api/v1/devices/sensor-42/command \
  -H "Content-Type: application/json" \
  -d '{"action":"reboot","params":{"delay_sec":5}}'
```
