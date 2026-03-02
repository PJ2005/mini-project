# Policy Engine

## Purpose

The policy engine (`internal/policy/policy.go`) evaluates every message flowing through the NATS bus and decides whether it should be forwarded. It runs as a NATS subscription interceptor wired by the gateway — adapters never interact with the policy engine directly.

## Key Decisions

1. **First-match-wins evaluation.** Rules are checked top-to-bottom. The first rule whose `device_pattern`, `message_type`, and `source_proto` all match determines the outcome.
2. **Glob patterns for device matching.** `device_pattern` uses `filepath.Match` syntax: `*` matches any single segment, `?` matches a single character.
3. **Empty or `"*"` means match-all.** Omitting a field from a rule is equivalent to `"*"` — it matches any value for that dimension.
4. **Configurable default action.** If no rule matches, `default_action` from config applies. Defaults to `allow` if unspecified.

## Rule Schema

```yaml
policy:
  default_action: "allow"   # "allow" or "deny" when no rules match
  rules:
    - device_pattern: "sensor-*"    # glob against device_id
      source_proto: "mqtt"          # exact match: "mqtt", "http", "" = any
      message_type: "telemetry"     # "telemetry", "command", "event", "" = any
      action: "allow"               # "allow" or "deny"
```

| Field | Required | Values | Description |
|-------|----------|--------|-------------|
| `device_pattern` | no | glob or `*` | Matches against `device_id` |
| `source_proto` | no | string or `*` | Matches against `source_proto` (mqtt, http, etc.) |
| `message_type` | no | string or `*` | Matches against payload type (telemetry, command, event) |
| `action` | yes | `allow`/`deny` | What to do when this rule matches |

## Evaluation Order

```
for each rule in rules (top to bottom):
    if device_pattern matches device_id
    AND source_proto matches message.source_proto
    AND message_type matches payload type:
        → return rule.action
return default_action
```

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

**Only allow specific sensors:**
```yaml
policy:
  default_action: "deny"
  rules:
    - device_pattern: "sensor-*"
      action: "allow"
    - device_pattern: "gateway-01"
      action: "allow"
```

**Block a single device:**
```yaml
policy:
  default_action: "allow"
  rules:
    - device_pattern: "rogue-device"
      action: "deny"
```
