# Device Registry

## Purpose

The registry (`internal/registry/registry.go`) is the single source of truth for known devices. Every adapter registers devices it encounters and updates their status as they connect, disconnect, or error. The registry is backed by SQLite (pure-Go `modernc.org/sqlite` — no CGo required) — no external database needed.

The registry manages three tables:
1. **`devices`** — device identity, status, and heartbeat tracking
2. **`latest_messages`** — persistent latest-message cache per device (survives restarts)
3. **`dead_letters`** — messages that failed to publish after retries

## Key Decisions

1. **Schema includes heartbeat tracking.** The `last_seen` column is updated on every message, enabling automatic heartbeat timeout detection:

| Column | Type | Description |
|--------|------|-------------|
| `device_id` | `TEXT PK` | Unique device identifier (set by adapter or device itself) |
| `name` | `TEXT` | Human-readable label |
| `protocol` | `TEXT` | Source adapter: `mqtt`, `http`, `coap`, etc. |
| `status` | `TEXT` | Lifecycle state: `active`, `inactive`, `error` |
| `metadata` | `TEXT` | JSON blob for adapter-specific key-value pairs |
| `created_at` | `DATETIME` | Auto-set on first registration |
| `last_seen` | `DATETIME` | Updated on every message — used for heartbeat timeout |

2. **Register is an upsert.** Calling `Register()` with an existing `device_id` updates `name`, `protocol`, `status`, `metadata`, and `last_seen` without losing `created_at`.

3. **Heartbeat timeout.** `MarkInactiveDevices(timeout)` queries devices where `last_seen < now - timeout` and `status = 'active'`, setting them to `inactive`. The gateway runs this check every 60 seconds.

4. **Persistent latest-message cache.** The `latest_messages` table stores the most recent serialized canonical message for each device. `UpsertLatestMessage()` and `GetLatestMessage()` replace the old in-memory map, so data survives restarts.

5. **Dead-letter table.** `InsertDeadLetter()` records messages that failed to publish after all retry attempts, enabling later inspection and replay.

6. **Pure-Go driver.** Uses `modernc.org/sqlite` instead of `go-sqlite3` — no CGo dependency, enabling cross-compilation without a C toolchain.

### Methods

| Method | Description |
|--------|-------------|
| `Register(device)` | Upsert device + update last_seen |
| `GetByID(deviceID)` | Returns nil (not error) for missing devices |
| `GetByProtocol(protocol)` | All devices matching a protocol |
| `UpdateStatus(deviceID, status)` | Update lifecycle state |
| `UpdateLastSeen(deviceID)` | Bump last_seen timestamp |
| `MarkInactiveDevices(timeout)` | Set inactive for devices not seen within timeout |
| `DeviceCount()` | Total registered device count |
| `DeviceCountByProtocol()` | Device counts grouped by protocol |
| `UpsertLatestMessage(deviceID, data)` | Store latest serialized message |
| `GetLatestMessage(deviceID)` | Retrieve latest serialized message |
| `InsertDeadLetter(subject, data, err)` | Record a failed publish attempt |

### Lifecycle States

```
  Register()           UpdateStatus("inactive")
  ┌──────┐             ┌───────────┐
  │      ▼             ▼           │
  │   active ──────► inactive     │
  │      │                         │  MarkInactiveDevices()
  │      │ UpdateStatus("error")   │  (heartbeat timeout)
  │      ▼                         │
  │    error ──────────────────────┘
  │      │   UpdateStatus("active")
  │      │
  └──────┘
     Register() resets to any status
```

## How To Extend

- **Add a column** — add it to the `CREATE TABLE` statement in `migrate()`, add it to the `Device` struct, and update `scanDevice` / `scanDeviceRow`.
- **New query** — add a method to `Registry` following the existing pattern.
- **Full-text search on metadata** — SQLite supports `json_extract()`. Example: `SELECT * FROM devices WHERE json_extract(metadata, '$.firmware') = '2.1.0'`.
