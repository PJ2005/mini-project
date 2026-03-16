# CoAP Adapter

## Purpose

The CoAP adapter (`internal/adapter/coap/coap.go`) provides ingestion for constrained IoT devices that communicate using the Constrained Application Protocol (RFC 7252) over UDP. It receives telemetry and event data, converts JSON payloads to canonical Protobuf messages, publishes to NATS, and registers devices in the SQLite registry.

CoAP is designed for low-power, low-bandwidth devices (e.g. battery-operated sensors, mesh networks) that cannot sustain TCP connections. The adapter runs a UDP server on the configured port (default `:5683`, the IANA-assigned CoAP port).

## CoAP Flow

```
CoAP Device                        InterLink                         NATS
    │                                │                               │
    │  POST /telemetry/sensor-99     │                               │
    │  {"metric":"humidity",         │                               │
    │   "value":62.1,"unit":"%"}     │                               │
    │ ──────────────────────────────►│                               │
    │                                │ validate device_id (Fix 10)   │
    │                                │ parse path → device_id        │
    │                                │ JSON → TelemetryPayload       │
    │                                │   metric="humidity"           │
    │                                │   value=62.1, unit="%"        │
    │                                │                               │
    │                                │ publish iot.telemetry.sensor-99
    │                                │ ─────────────────────────────►│
    │                                │                               │
    │                                │ registry.Register(sensor-99)  │
    │                                │                               │
    │  2.01 Created                  │                               │
    │  {"message_id":"a1b2c3..."}    │                               │
    │ ◄──────────────────────────────│                               │
```

## Device ID Validation (Fix 10)

Before processing any request, the adapter validates the extracted device ID:
- **Empty check** — if the device ID is empty or missing from the path, returns CoAP `4.00 Bad Request` with message `"device_id is empty or missing from path"`.
- **Character validation** — device IDs must match `^[a-zA-Z0-9_-]+$` (alphanumerics, dashes, and underscores only). Invalid characters return `4.00 Bad Request` with a descriptive message including the offending characters.

## Endpoints

### POST /telemetry/{device_id}

Ingests telemetry data. The `device_id` is extracted from the URI path and validated.

**Request body (JSON):**
```json
{"metric": "humidity", "value": 62.1, "unit": "%"}
```

**Response:** `2.01 Created` with `{"message_id": "..."}`.

### POST /event/{device_id}

Ingests event data for alerts, alarms, and one-shot occurrences.

**Request body (JSON):**
```json
{"event_type": "alarm", "severity": "critical", "detail": "temperature exceeded threshold"}
```

**Response:** `2.01 Created` with `{"message_id": "..."}`.

## Configuration

```yaml
coap:
  listen: ":5683"   # Standard CoAP UDP port (IANA assigned)
```

## Key Decisions

1. **UDP only.** CoAP over UDP is the standard transport for constrained devices. DTLS can be added via `coap.ListenAndServeDTLS` when encryption is needed.
2. **JSON payloads.** Although CoAP typically uses CBOR, JSON is used here for consistency with the MQTT and HTTP adapters. A CBOR extension can be added by checking the Content-Format option.
3. **Same path convention as HTTP.** `/telemetry/{device_id}` and `/event/{device_id}` mirror the HTTP ingest API, reducing cognitive load.
4. **go-coap/v3.** The `plgd-dev/go-coap/v3` library provides a mux-based router with the same `Handler`/`ResponseWriter` pattern as Go's `net/http`.
5. **Strict device ID validation.** Only alphanumerics, dashes, and underscores are accepted, preventing path traversal and injection attacks.

## Testing with coap-client

**Send telemetry:**
```bash
coap-client -m post coap://localhost:5683/telemetry/sensor-99 \
  -e '{"metric":"humidity","value":62.1,"unit":"%"}'
```

**Send event:**
```bash
coap-client -m post coap://localhost:5683/event/sensor-99 \
  -e '{"event_type":"alarm","severity":"critical","detail":"overheated"}'
```

> `coap-client` is part of [libcoap](https://libcoap.net/). Alternatively, use the Go CoAP client from `plgd-dev/go-coap/v3/udp`.

## How To Extend

- **CBOR support** — check `req.Options().ContentFormat()` for `message.AppCBOR` and decode with `encoding/cbor` before converting to canonical.
- **DTLS encryption** — switch from `coap.ListenAndServe` to `coap.ListenAndServeDTLS` with a DTLS config. No handler changes needed.
- **Observe (RFC 7641)** — add an observable resource for command delivery, allowing constrained devices to receive commands via CoAP observe notifications.
