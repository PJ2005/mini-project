# DEVELOPMENT

## Current Implementation

### Build, Test, and Verification Status
- Build command run: `go build ./...`
- First run error (environment permission, not code compile):
  - `error acquiring upload token: creating token file: open C:\Users\prath\AppData\Roaming\go\telemetry\local\upload.token: Access is denied.`
  - `go: failed to trim cache: open C:\Users\prath\AppData\Local\go-build\trim.txt: Access is denied.`
- Fix applied: rerun build with elevated permission for Go cache/telemetry path access.
- Final build status: PASS
- Test command run: `go test ./...`
- Test status: PASS
  - `ok   interlink/internal/bus`
  - `ok   interlink/internal/canonical`
  - `ok   interlink/internal/gateway`
  - `ok   interlink/internal/pipeline`
  - `ok   interlink/internal/policy`
  - `ok   interlink/internal/registry`
  - `ok   interlink/internal/ui`
  - no-test packages: `cmd/gateway`, `internal/adapter`, `internal/adapter/coap`, `internal/adapter/http`, `internal/adapter/mqtt`, `internal/metrics`, `tools/loadtest`

### Component: `cmd/gateway`
- Purpose: process entrypoint. Parse `-config`, initialize JSON slog logger, call gateway runtime.
- Key types/functions/interfaces:
  - `main()`
- Dependencies:
  - `interlink/internal/gateway`
  - stdlib `flag`, `log/slog`, `os`
- Known gaps:
  - logging level/config not externally configurable from config file.

### Component: `internal/adapter` (base interface)
- Purpose: shared adapter contract across protocol adapters.
- Key types/functions/interfaces:
  - `type Adapter interface { Name(); Start(...); Stop(...) }`
  - `type ConverterFunc`
- Dependencies:
  - `interlink/internal/bus`
  - `interlink/internal/registry`
  - stdlib `context`
- Known gaps:
  - no lifecycle health/readiness hook in interface.

### Component: `internal/adapter/mqtt`
- Purpose: ingest MQTT payloads, convert JSON numeric telemetry to canonical protobuf, publish to NATS, auto-register device.
- Key types/functions/interfaces:
  - `type Config`
  - `type Adapter`
  - `New`, `Start`, `Stop`, `onMessage`, `runMessagePump`, `processMessage`
  - `extractDeviceID`, `convertPayload`
- Dependencies:
  - `github.com/eclipse/paho.mqtt.golang`
  - `interlink/internal/bus`
  - `interlink/internal/canonical`
  - `interlink/internal/metrics`
  - `interlink/internal/registry`
- Known gaps:
  - converter only emits first numeric field found (top-level or 1-level nested).
  - non-numeric MQTT payload content dropped.
  - adapter handles telemetry only (no MQTT command/event canonical mapping).
  - message channel fixed size (`1024`), overflow drops messages.

### Component: `internal/adapter/http`
- Purpose: HTTP adapter and API surface (`ingest`, `latest`, `stream`, `command`, `health`, `metrics`), SSE fanout, command request/reply.
- Key types/functions/interfaces:
  - `type Config`
  - `type Adapter`
  - `New`, `Start`, `Stop`
  - handlers: `handleIngest`, `handleLatest`, `handleStream`, `handleCommand`, `handleHealth`
  - SSE helpers + metrics middleware
  - `SetAdapterNames`, `SetPolicyEngine`, `StartGaugeRefresh`
- Dependencies:
  - `interlink/internal/bus`
  - `interlink/internal/canonical`
  - `interlink/internal/metrics`
  - `interlink/internal/policy`
  - `interlink/internal/registry`
  - `github.com/prometheus/client_golang/prometheus/promhttp`
- Known gaps:
  - no authn/authz on API endpoints.
  - SSE storage keyed only by `device_id`; no per-client backpressure recovery besides drop.
  - command ACK payload passthrough is unvalidated JSON bytes.

### Component: `internal/adapter/coap`
- Purpose: UDP CoAP ingest for telemetry/event path, publish canonical protobuf to NATS, auto-register device.
- Key types/functions/interfaces:
  - `type Config`
  - `type Adapter`
  - `New`, `Start`, `Stop`
  - handlers: `handleTelemetry`, `handleEvent`
  - helpers: `validateDeviceID`, `extractDeviceID`, `readBody`, `setResponse`
- Dependencies:
  - `github.com/plgd-dev/go-coap/v3` (+ mux/message/codes)
  - `interlink/internal/bus`
  - `interlink/internal/canonical`
  - `interlink/internal/metrics`
  - `interlink/internal/registry`
- Known gaps:
  - server start uses `ListenAndServe` goroutine; `Stop` cancels context but does not explicitly close coap listener.
  - payload validation minimal (shape assumed by struct decode).

### Component: `internal/bus`
- Purpose: messaging abstraction + NATS and JetStream implementations; publish retry + dead-letter helper.
- Key types/functions/interfaces:
  - `type MessageBus`
  - `type Subscription`
  - `type NATSBus`, `Connect`, `Publish`, `Subscribe`, `Request`, `IsConnected`, `Close`
  - `type JetStreamBus`, `ConnectJetStream`, same bus methods
  - `PublishWithRetry`
- Dependencies:
  - `github.com/nats-io/nats.go`
  - `interlink/internal/metrics`
  - `interlink/internal/registry`
- Known gaps:
  - `Close()` ignores drain error return.
  - JetStream durable name uses raw subject (`"interlink-"+subject`); wildcard subjects can produce invalid durable names (covered by current test expecting subscribe error).
  - retry helper backoff/attempt count fixed, not config-driven.

### Component: `proto/canonical.proto` and `internal/canonical`
- Purpose: canonical message model and protobuf marshaling utilities.
- Key types/functions/interfaces:
  - proto messages: `TelemetryPayload`, `CommandPayload`, `EventPayload`, `Message` (`oneof payload`, `metadata` map)
  - generated types in `canonical.pb.go`
  - helper constructors: `NewTelemetryMessage`, `NewEventMessage`, `NewCommandMessage`
  - serialization: `Marshal`, `MarshalPooled`, `UnmarshalMessage`
  - routing helpers: `SubjectType`, `Subject`
- Dependencies:
  - `google.golang.org/protobuf`
  - stdlib crypto/time/sync
- Known gaps:
  - `generateID()` ignores `rand.Read` error path.
  - `Marshal` and `MarshalPooled` currently same behavior (duplicate helper surface).
  - no explicit schema version field in canonical message.

### Component: `internal/policy`
- Purpose: rule-based allow/deny evaluation, worker-queue processing, hot-reload from config file via fsnotify, optional command trigger on allow.
- Key types/functions/interfaces:
  - `type Rule`, `type Config`, `type Engine`, `type Counters`
  - `New`, `SetBus`, `StartWorkerPool`, `Submit`, `Evaluate`
  - `WatchConfig`, `reload`
  - matcher `matchField`
- Dependencies:
  - `github.com/fsnotify/fsnotify`
  - `gopkg.in/yaml.v3`
  - `interlink/internal/bus`
  - `interlink/internal/canonical`
  - `interlink/internal/metrics`
- Known gaps:
  - `submitCh` fixed queue (`2048`), full queue drops.
  - matching uses `filepath.Match` wildcard semantics only.
  - default action normalization not forced to lowercase in engine constructor path.

### Component: `internal/registry`
- Purpose: SQLite-backed device registry, latest-message cache/persistence, dead-letter storage, device status/heartbeat queries.
- Key types/functions/interfaces:
  - `type Device`, `type Registry`
  - `New`, `Close`, `Register`, `GetByID`, `GetByProtocol`, `ListDevices`
  - `UpdateStatus`, `UpdateLastSeen`, `MarkInactiveDevices`
  - counters: `DeviceCount`, `DeviceCountByProtocol`, `DeviceCountByStatus`
  - latest message: `UpsertLatestMessage`, `GetLatestMessage`, `LatestMessageCount`
  - dead letter: `InsertDeadLetter`, `DeadLetterCount`
  - metrics helper: `DBPath`
- Dependencies:
  - `modernc.org/sqlite`
  - stdlib `database/sql`, `encoding/json`, `sync`, `time`
- Known gaps:
  - write queue (`writeCh` size `512`) can block producer goroutine when saturated.
  - no retention/cleanup policy for `dead_letters` or `latest_messages`.
  - single DB writer (`SetMaxOpenConns(1)`) good for contention safety, limits write throughput under heavy load.

### Component: `internal/metrics`
- Purpose: Prometheus metric definitions, registration, runtime/storage collector, health snapshots.
- Key types/functions/interfaces:
  - metric vars (`CounterVec`, `Gauge`, `Histogram`)
  - `Init`, `StartCollector`
  - `RecordReceive`, `RecordPublish`, `GetAdapterCounts`
  - snapshot helpers: `RuntimeSnapshot`, `StorageSnapshot`, `ThroughputSnapshot`, `Uptime`
  - `type DBStatsProvider`
- Dependencies:
  - `github.com/prometheus/client_golang/prometheus`
  - stdlib runtime/os/sync/time
- Known gaps:
  - `Init()` uses `prometheus.MustRegister`; repeated init in same process would panic.
  - collector interval externally passed but no runtime reconfiguration.

### Component: `internal/pipeline`
- Purpose: YAML-driven source->transform->sink data pipeline with optional Tengo script transform and runtime stats.
- Key types/functions/interfaces:
  - config types: `PipelineConfig`, `SourceConfig`, `TransformConfig`, `SinkConfig`
  - runtime types: `Pipeline`, `Stats`, `Record`
  - lifecycle: `New`, `Start`, `Stop`, `Stats`
  - execution: `handleMessage`, `dispatch`, `RunTransform`, `RunChain`
  - script engine: `CompileScript`, `RunScript`
- Dependencies:
  - `interlink/internal/bus`
  - `interlink/internal/canonical`
  - `interlink/internal/registry`
  - `github.com/d5/tengo/v2`
  - stdlib `net/http`, `encoding/json`, sync/time
- Known gaps:
  - pipeline processes telemetry only (`canon.GetTelemetry()` required).
  - source/sink protocol support constrained to coded enum (`nats`/`mqtt` source, `http_post`/`nats_publish`/`mqtt_publish` sink).
  - script timeout fixed at `5ms`; not config-driven.
  - sink `mqtt_publish` currently routes through bus publish abstraction, not direct MQTT client.
  - source queue fixed (`1024`), overflow drops.

### Component: `internal/ui`
- Purpose: lightweight read-only HTML dashboard and JSON APIs for pipeline stats + device list.
- Key types/functions/interfaces:
  - `type UIServer`
  - `New`, `Handler`
  - handlers: `handleIndex`, `handlePipelines`, `handleDevices`
- Dependencies:
  - `interlink/internal/pipeline`
  - `interlink/internal/registry`
  - stdlib `net/http`, `encoding/json`
- Known gaps:
  - no authentication.
  - static embedded HTML only; no template or asset pipeline.
  - API error handling intentionally minimal (registry errors collapse to empty list).

### Component: `internal/gateway`
- Purpose: full runtime orchestration. Load/validate config, initialize metrics/bus/registry/policy/adapters/pipelines/UI, manage graceful shutdown and heartbeat inactivity sweeper.
- Key types/functions/interfaces:
  - config types: `Config`, `NATSConfig`, `RegistryConfig`, `UIConfig`, `Duration`
  - runtime: `Run`, `loadConfig`, `validateConfig`
- Dependencies:
  - adapters (`mqtt`, `http`, `coap`)
  - `interlink/internal/bus`
  - `interlink/internal/metrics`
  - `interlink/internal/pipeline`
  - `interlink/internal/policy`
  - `interlink/internal/registry`
  - `interlink/internal/ui`
  - `gopkg.in/yaml.v3`
- Known gaps:
  - config validation fatal path calls `os.Exit(1)` inside `Run`, reducing composability/testability.
  - UI log line prints fixed localhost URL text while actual bind comes from `cfg.UI.Listen`.

### Component: `tools/loadtest`
- Purpose: standalone MQTT load generator and benchmark reporter for InterLink throughput/latency + `/health` snapshot.
- Key types/functions/interfaces:
  - `type Config`, `type Result`, `type ThroughputR`, `type LatencyR`
  - `main`, `fetchHealth`, percentile helpers
- Dependencies:
  - `github.com/eclipse/paho.mqtt.golang`
  - stdlib `flag`, `net/http`, `encoding/json`, `sync`, `time`
- Known gaps:
  - assumes external broker and reachable health endpoint.
  - no TLS/auth flags for secured brokers.
  - message split uses integer division (`messages/concurrency`), potential remainder unsent.

### Config and Build Artifacts
- `config/config.yaml`
  - Purpose: default runtime config for NATS, MQTT, HTTP, CoAP, UI, registry, policy, heartbeat, optional pipeline examples.
  - Known gap: several operational values hard-coded comments only; no schema validation file.
- `go.mod`
  - Purpose: module + dependency definitions.
  - Notable deps: NATS, MQTT, CoAP, SQLite, Prometheus, fsnotify, Tengo, protobuf.
- `Makefile`
  - Purpose: build/run/proto/clean commands.
  - Known gaps:
    - `run` target executes `./interlink-gateway` (POSIX-style path); direct portability issue on native Windows shells.
    - `clean` target uses `rm -f` (POSIX utility, not native PowerShell/cmd).

## Planned Phases
- [x] Phase 1: Add incoming adapters
  - [x] Modbus TCP adapter (`internal/adapter/modbus.go`)
  - [x] WebSocket adapter (`internal/adapter/websocket.go`)
  - [x] AMQP adapter (`internal/adapter/amqp.go`)
  - [x] Gateway wiring + config surface updates
  - [x] Smoke-test documentation
- [ ] Phase 2+: _TBD in next prompts_

## Change Log
### Phase 1 — 2026-04-16
- Created:
  - `internal/adapter/modbus.go`
  - `internal/adapter/websocket.go`
  - `internal/adapter/amqp.go`
  - `docs/SMOKE_TESTS.md`
- Modified:
  - `internal/gateway/gateway.go`
  - `config/config.yaml`
  - `go.mod`
  - `go.sum`
  - `docs/DEVELOPMENT.md`
- Deviations from plan:
  - New adapters are conditionally started only when minimally configured (`modbus.host + registers`, `websocket.listen`, `amqp.url + queue`) so default config remains runnable.
  - No standalone adapter factory existed; adapters added to gateway adapter startup list.
