# Policy Engine

## Purpose

The policy engine (`internal/policy/policy.go`) evaluates every message flowing through the NATS bus and decides whether it should be forwarded. It runs as a NATS subscription interceptor wired by the gateway — adapters never interact with the policy engine directly.

## Features

- **First-match-wins evaluation** — rules are checked top-to-bottom
- **Glob patterns** — `filepath.Match` syntax for device matching
- **Hot-reload** — `fsnotify` watches `config.yaml` and atomically swaps rules on change (no restart needed)
- **Command routing** — matching rules can trigger commands to other devices (device-to-device routing)
- **Allow/deny counters** — tracked for Prometheus metrics export

## Key Decisions

1. **First-match-wins evaluation.** Rules are checked top-to-bottom. The first rule whose `device_pattern`, `message_type`, and `source_proto` all match determines the outcome.
2. **Glob patterns for device matching.** `device_pattern` uses `filepath.Match` syntax: `*` matches any single segment, `?` matches a single character.
3. **Empty or `"*"` means match-all.** Omitting a field from a rule is equivalent to `"*"` — it matches any value for that dimension.
4. **Configurable default action.** If no rule matches, `default_action` from config applies. Defaults to `allow` if unspecified.
5. **Thread-safe hot-reload.** Rules are protected by `sync.RWMutex`. `Evaluate` reads under RLock; reload swaps under full Lock.

## Rule Schema

```yaml
policy:
  default_action: "allow"   # "allow" or "deny" when no rules match
  rules:
    - device_pattern: "sensor-*"    # glob against device_id
      source_proto: "mqtt"          # exact match: "mqtt", "http", "" = any
      message_type: "telemetry"     # "telemetry", "command", "event", "" = any
      action: "allow"               # "allow" or "deny"
      # Optional: device-to-device command routing (Fix 4)
      command_target: "actuator-01" # target device for command
      command_action: "adjust"      # command action to send
```

| Field | Required | Values | Description |
|-------|----------|--------|-------------|
| `device_pattern` | no | glob or `*` | Matches against `device_id` |
| `source_proto` | no | string or `*` | Matches against `source_proto` (mqtt, http, etc.) |
| `message_type` | no | string or `*` | Matches against payload type (telemetry, command, event) |
| `action` | yes | `allow`/`deny` | What to do when this rule matches |
| `command_target` | no | device ID | Target device for command routing (requires `action: allow`) |
| `command_action` | no | string | Command action to publish to the target device |

## Evaluation Order

```
for each rule in rules (top to bottom):
    if device_pattern matches device_id
    AND source_proto matches message.source_proto
    AND message_type matches payload type:
        → return rule.action
        → if action is "allow" and command_target is set:
           publish command to iot.command.<command_target>
return default_action
```

## Hot-Reload (Fix 5)

The engine watches the config directory using `fsnotify`. When `config.yaml` is written or created (covering both direct edits and atomic save patterns), the policy section is re-parsed and the rules are atomically swapped. No gateway restart is needed.

## Config Examples

**Allow all (default):**
```yaml
policy:
  default_action: "allow"
  rules: []
```

**Deny commands from MQTT devices, allow everything else:**
```yaml
policy:
  default_action: "allow"
  rules:
    - source_proto: "mqtt"
      message_type: "command"
      action: "deny"
```

**Device-to-device command routing:**
```yaml
policy:
  default_action: "allow"
  rules:
    - device_pattern: "sensor-01"
      message_type: "telemetry"
      action: "allow"
      command_target: "actuator-01"
      command_action: "adjust"
```

When `sensor-01` sends telemetry, the policy engine also publishes a `adjust` command to `iot.command.actuator-01`.
