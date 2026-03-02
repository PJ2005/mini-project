# Device Registry

## Purpose

The registry (`internal/registry/registry.go`) is the single source of truth for known devices. Every adapter registers devices it encounters and updates their status as they connect, disconnect, or error. The registry is backed by SQLite — no external database required.

The registry answers two questions:
1. **What devices exist?** — `GetByID`, `GetByProtocol`
2. **What state are they in?** — `Status` field on every device record

## Key Decisions

1. **Schema is minimal by design.** Six columns cover identity, protocol origin, lifecycle state, and extensibility:

| Column | Type | Description |
|--------|------|-------------|
| `device_id` | `TEXT PK` | Unique device identifier (set by adapter or device itself) |
| `name` | `TEXT` | Human-readable label |
| `protocol` | `TEXT` | Source adapter: `mqtt`, `http`, `coap`, etc. |
| `status` | `TEXT` | Lifecycle state: `active`, `inactive`, `error` |
| `metadata` | `TEXT` | JSON blob for adapter-specific key-value pairs |
| `created_at` | `DATETIME` | Auto-set on first registration |

2. **Register is an upsert.** Calling `Register()` with an existing `device_id` updates `name`, `protocol`, `status`, and `metadata` without losing `created_at`. This lets adapters call `Register` on every message without worrying about duplicates.

3. **Status is a plain string, not an enum.** This keeps the registry protocol-agnostic. Standard values are `active`, `inactive`, and `error`, but adapters may define others (e.g. `upgrading`).

4. **Metadata is a JSON blob.** Rather than adding columns for every adapter-specific field, the `metadata` column stores `map[string]string` as JSON. The registry marshals/unmarshals automatically.

5. **GetByID returns nil (not error) for missing devices.** This lets callers distinguish "not found" from "database error" without sentinel errors.

### Lifecycle States

```
  Register()           UpdateStatus("inactive")
  ┌──────┐             ┌───────────┐
  │      ▼             ▼           │
  │   active ──────► inactive     │
  │      │                         │
  │      │ UpdateStatus("error")   │
  │      ▼                         │
  │    error ──────────────────────┘
  │      │   UpdateStatus("active")
  │      │
  └──────┘
     Register() resets to any status
```

Transitions are not enforced — callers set whatever status is appropriate. The diagram above shows the typical flow.

## How To Extend

- **Add a column** — add it to the `CREATE TABLE` statement in `migrate()`, add it to the `Device` struct, and update `scanDevice` / `scanDeviceRow`. Use `ALTER TABLE` in a versioned migration if data already exists.
- **New query** — add a method to `Registry` (e.g. `ListAll`, `DeleteByID`). Follow the existing pattern: SQL query → `scanDevice` / `scanDeviceRow`.
- **Full-text search on metadata** — SQLite supports `json_extract()`. Example: `SELECT * FROM devices WHERE json_extract(metadata, '$.firmware') = '2.1.0'`.
