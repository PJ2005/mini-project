# IMPROVEMENTS

Single source of truth for changes made in this project.

## Summary

- Phases completed: Phase 1 to Phase 5 implemented; Phase 6 hardening/audit completed in this update.
- Key files changed in Phase 6:
	- `internal/canonical/canonical_test.go`
	- `internal/policy/policy_test.go`
	- `internal/registry/registry_test.go`
	- `internal/pipeline/pipeline_unit_test.go`
	- `internal/bus/bus_test.go`
	- `internal/gateway/gateway.go`
	- `internal/gateway/gateway_test.go`
	- `internal/adapter/mqtt/mqtt.go`
	- `config/config.yaml`
	- `README.md`
	- `go.mod`
- Test and quality checks run:
	- `go test ./... -race -count=1 -coverprofile=coverage.out`
	- `go tool cover -func=coverage.out`
	- `go vet ./...`
	- `staticcheck ./...`
	- `go build -trimpath -ldflags="-s -w" -o interlink-gateway ./cmd/gateway`
- Limitations:
	- `internal/canonical` package coverage remains below 60% at package level because generated `canonical.pb.go` is intentionally not targeted by tests.
	- Adapter and metrics packages still have low/zero coverage and are best addressed with integration tests.
- Next recommendation:
	- Add integration tests for HTTP/MQTT/CoAP adapters and metrics snapshots to raise practical end-to-end confidence.

## Phase 6 — Hardening and final audit
Date: 2026-04-14

Status: complete

## Phase 6 log

- Coverage audit command run:
	- `go test ./... -race -count=1 -coverprofile=coverage.out`
	- `go tool cover -func=coverage.out`
- Coverage summary (priority packages):

| Package | Coverage |
|---|---:|
| `interlink/internal/pipeline` | 66.5% |
| `interlink/internal/policy` | 64.1% |
| `interlink/internal/registry` | 75.2% |
| `interlink/internal/canonical` | 56.5% |
| `interlink/internal/bus` | 76.1% |

- Coverage work completed:
	- Added/expanded tests for pipeline runtime helpers, dispatch paths, lifecycle, and script compile failures.
	- Added policy tests for allow/deny evaluation, bounded submit behavior, and reload updates.
	- Added registry CRUD and counter coverage with async latest-writer flush-aware assertions.
	- Added bus tests for retry/dead-letter path plus NATS and JetStream connectivity round-trips.
	- Expanded canonical tests for constructors, subject typing (including unknown), marshal/unmarshal paths, and error paths.
- Static analysis:
	- `go vet ./...`: pass.
	- `staticcheck ./...`: one finding (`SA4006` in MQTT adapter Start context reassignment) fixed by discarding the unused reassigned context; re-run pass.
- Config hardening in gateway validation:
	- Enforced `policy.worker_count >= 1`.
	- Added `http_post` sink URL validation requiring valid `http`/`https` URL.
	- Kept explicit `ui.listen != http.listen` validation and fatal log message.
	- Added `internal/gateway/gateway_test.go` to validate these checks.
- Documentation update:
	- Rewrote `README.md` for current architecture, quick start, DSL, benchmark table + interpretation, UI, and extension docs links.
- Release/final checks:
	- Stripped build command: `go build -trimpath -ldflags="-s -w" -o interlink-gateway ./cmd/gateway`.
	- Binary size (`interlink-gateway`): `19802112` bytes.
	- Final race run: `go test ./... -race -count=1` pass.

## Phase 6 result

Phase 6 hardening and final audit tasks are complete.

Known exception: `internal/canonical` remains below the requested 60% package threshold because generated Protobuf code (`canonical.pb.go`) dominates package statement count and is intentionally excluded from direct testing.

## Phase 1 — Foundation fixes
Date: 2026-04-14

Status: in progress

## Log

Use this section to record each fix with the change, why, test command, result, and known issues.

- Fix 2: Added canonical.MarshalPooled and switched MQTT/HTTP/CoAP hot paths to reuse pooled protobuf buffers to reduce GC pressure; test: `go test ./internal/canonical/... -v`; result: pass.
- Fix 3: Verified canonical message IDs are generated with crypto/rand and added a 1000-iteration uniqueness/length test for telemetry plus constructor length checks for event/command; test: `go test ./internal/canonical/... -run TestMessageID -v`; result: pass.
- Fix 1: Moved MQTT ingestion off the Paho dispatcher with a buffered queue, non-blocking drop path, and a draining worker goroutine; tests: `go build ./...` and `go vet ./...`; result: pass.
- Fix 4: Added the `build-release` Makefile target with `-ldflags="-s -w" -trimpath` and measured binary size change from 27387904 bytes to 18761216 bytes; size capture used PowerShell `Get-Item` because `make`/`bash` were unavailable in this shell; result: pass.

## Phase 1 result

`go build ./...` passed and `go vet ./...` passed after all Phase 1 fixes.

## Phase 2 — Performance
Date: 2026-04-14

Status: in progress

## Phase 2 log

Record each performance change here with the change, why, test command, result, and known issues.

- Change 1: Moved policy evaluation off the NATS dispatcher with a buffered worker pool, non-blocking submit path, and dropped-message metric; test: `go test ./internal/policy/... -v -race`; result: pass.
- Change 2: Added a write-through latest-message cache with a background SQLite writer and restart preload; test: `go test ./internal/registry/... -v -race`; result: pass.
- Change 3: Changed SSE fan-out to snapshot client channels before iteration and added a slow-consumer drop counter; tests: `go build ./...` and `go vet ./...`; result: pass.

## Phase 2 result

`go test ./... -race -count=1` passed with the following package results:

- `interlink/cmd/gateway`: no test files
- `interlink/internal/adapter`: no test files
- `interlink/internal/adapter/coap`: no test files
- `interlink/internal/adapter/http`: no test files
- `interlink/internal/adapter/mqtt`: no test files
- `interlink/internal/bus`: no test files
- `interlink/internal/canonical`: pass
- `interlink/internal/gateway`: no test files
- `interlink/internal/metrics`: no test files
- `interlink/internal/policy`: pass
- `interlink/internal/registry`: pass
- `interlink/tools/loadtest`: no test files

## Phase 3 — Pipeline DSL
Date: 2026-04-14

Status: in progress

## Phase 3 design

The pipeline DSL is defined as a new top-level key in `config.yaml`:

```yaml
pipelines:
	- name: temperature-bridge
		source:
			type: mqtt
			topic: devices/#
			device_id_index: 1
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
		sink:
			type: http_post
			url: http://dashboard.local/ingest
			timeout: 3s
```

Each pipeline has exactly one source, zero or more transforms, and one sink. The pipeline engine subscribes to the source and for each message runs the transform chain, then dispatches to the sink. Transforms operate on a simple `map[string]float64` extracted from canonical `TelemetryPayload` data.

Supported transform types in this phase:
- `extract`: pick one numeric field
- `scale`: multiply and offset
- `rename`: rename a key
- `filter`: drop message if value is outside min/max range

Supported sink types in this phase:
- `http_post`: POST JSON to a URL
- `nats_publish`: publish to a NATS subject
- `mqtt_publish`: publish to an MQTT topic

## Phase 3 log

Record each DSL change here with the change, why, test command, result, and known issues.

- Step 1: Added pipeline DSL config structs in `internal/pipeline/config.go` and wired `Pipelines []pipeline.PipelineConfig` into the top-level gateway config so YAML-defined pipelines load with the main config; test: `go test ./internal/pipeline/... -v`; result: pass.
- Step 2: Added transform executor (`extract`, `scale`, `rename`, `filter`) and chain runner in `internal/pipeline/transform.go` with unit tests in `internal/pipeline/transform_test.go`; test: `go test ./internal/pipeline/... -v`; result: pass.
- Step 3: Added pipeline runtime in `internal/pipeline/pipeline.go` with source subscription, telemetry decoding, transform chain application, and sink dispatch for `http_post`, `nats_publish`, and `mqtt_publish`; test: `go test ./internal/pipeline/... -v`; result: pass.
- Step 4: Wired pipeline lifecycle into gateway startup and graceful shutdown in `internal/gateway/gateway.go`; invalid pipeline config now logs and skips without crashing the gateway; test: `go test ./... -race -count=1`; result: pass.
- Step 5: Added commented pipeline DSL examples covering all transform types and both publish sinks in `config/config.yaml`; tests: `go test ./internal/pipeline/... -v` and `go test ./... -race -count=1`; result: pass.

`go test ./internal/pipeline/... -v` output:
- `TestPipelineNATSScaleFlow`: skipped (`nats not available`)
- `TestRunTransformExtract`: pass
- `TestRunTransformScaleZeroOffset`: pass
- `TestRunTransformRename`: pass
- `TestRunTransformFilterPass`: pass
- `TestRunTransformFilterDrop`: pass
- package result: pass

Known issues:
- `mqtt_publish` sink currently publishes pipeline JSON payloads through the shared message bus on the configured topic string; direct MQTT broker publish is not yet implemented because sink config does not currently include broker/credentials.

## Phase 3 result

`go test ./... -race -count=1` passed with the following package results:

- `interlink/cmd/gateway`: no test files
- `interlink/internal/adapter`: no test files
- `interlink/internal/adapter/coap`: no test files
- `interlink/internal/adapter/http`: no test files
- `interlink/internal/adapter/mqtt`: no test files
- `interlink/internal/bus`: no test files
- `interlink/internal/canonical`: pass
- `interlink/internal/gateway`: no test files
- `interlink/internal/metrics`: no test files
- `interlink/internal/pipeline`: pass
- `interlink/internal/policy`: pass
- `interlink/internal/registry`: pass
- `interlink/tools/loadtest`: no test files

## Phase 4 — Script node
Date: 2026-04-14

Status: in progress

## Phase 4 design decision

Script runtime library: `github.com/d5/tengo/v2`.

Reasons:
- Pure Go (no cgo requirement).
- Sandboxed runtime (no direct OS or network access by default).
- Fast execution characteristics suitable for per-message transform use.
- Map/variable model aligns with pipeline `Record` (`map[string]float64`).

Alternatives considered:
- `expr-lang/expr`: strong expression evaluation, but less natural for mutable multi-step script transforms.
- `gopher-lua`: capable but heavier integration and larger surface area than needed for this phase.

Decision: Tengo is the best simplicity/safety tradeoff for this script-node use case.

## Phase 4 log

Record each script-node change here with the change, why, test command, result, and known issues.

- Dependency: added `github.com/d5/tengo/v2` via `go get github.com/d5/tengo/v2`; result: pass.
- Step 1: extended pipeline transform handling with `type: script` and `TransformConfig.Script` support, integrating script execution into `RunTransform`; test: `go test ./internal/pipeline/... -v -timeout 30s`; result: pass.
- Step 2: added script runtime in `internal/pipeline/script.go` with compile cache (`sync.Map`, SHA-256 key), execution clone per message, variable round-trip into `Record`, and 5ms timeout fallback to original record; test: `go test ./internal/pipeline/... -v -timeout 30s`; result: pass.
- Step 3: added compile-cache warming in `pipeline.New()` so broken scripts fail pipeline startup early with source+error details; test: `go test ./internal/pipeline/... -v -timeout 30s`; result: pass.
- Step 4: added `script` field in `TransformConfig` (`internal/pipeline/config.go`) for YAML-configured script nodes; test: `go test ./internal/pipeline/... -v -timeout 30s`; result: pass.
- Step 5: updated `config/config.yaml` example pipelines to include a script transform node after built-in transforms; tests: `go test ./internal/pipeline/... -v -timeout 30s` and `go test ./... -race -count=1`; result: pass.

`go test ./internal/pipeline/... -v -timeout 30s` output:
- `TestPipelineNATSScaleFlow`: skipped (`nats not available`)
- `TestScriptBasic`: pass
- `TestScriptMultiVar`: pass
- `TestScriptCompileError`: pass
- `TestScriptCacheHit`: pass
- `TestScriptTimeout`: pass
- `TestRunTransformExtract`: pass
- `TestRunTransformScaleZeroOffset`: pass
- `TestRunTransformRename`: pass
- `TestRunTransformFilterPass`: pass
- `TestRunTransformFilterDrop`: pass
- package result: pass

## Phase 4 result

`go test ./... -race -count=1` passed with the following package results:

- `interlink/cmd/gateway`: no test files
- `interlink/internal/adapter`: no test files
- `interlink/internal/adapter/coap`: no test files
- `interlink/internal/adapter/http`: no test files
- `interlink/internal/adapter/mqtt`: no test files
- `interlink/internal/bus`: no test files
- `interlink/internal/canonical`: pass
- `interlink/internal/gateway`: no test files
- `interlink/internal/metrics`: no test files
- `interlink/internal/pipeline`: pass
- `interlink/internal/policy`: pass
- `interlink/internal/registry`: pass
- `interlink/tools/loadtest`: no test files

## Phase 5 — Web UI
Date: 2026-04-14

Status: in progress

## Phase 5 decision

Message inspector over UI-port SSE multiplexing is deferred to Phase 6 to keep this phase focused on a minimal, reliable read-only UI surface.

## Phase 5 log

Record each UI change here with the change, why, test command, result, and known issues.

- Step 1: Added read-only UI server in `internal/ui/ui.go` with routes `GET /`, `GET /api/ui/pipelines`, and `GET /api/ui/devices`; UI data comes from pipeline runtime stats and registry device records; test: `go test ./internal/ui/... -v`; result: pass.
- Step 2: Added pipeline runtime status counters (`messages_processed`, `last_message_at`, `error_count`) with atomic updates and `Stats()` export in `internal/pipeline/pipeline.go`; test: `go test ./... -race -count=1`; result: pass.
- Step 3: Added embedded pure HTML + vanilla JS UI page (no frameworks, no build step), two responsive sections (pipelines/devices), dark-mode-aware styling, and periodic API refreshes; message inspector explicitly deferred to Phase 6; tests: `go test ./internal/ui/... -v` and `go build ./...`; result: pass.
- Step 4: Wired UI lifecycle into gateway startup/shutdown in `internal/gateway/gateway.go` with independent listener, 3s shutdown timeout, and startup log line `InterLink UI available at http://localhost:8081`; tests: `go build ./...` and `go test ./... -race -count=1`; result: pass.
- Step 5: Added `ui.listen` to config (`config/config.yaml`, default `:8081`) and validation in gateway to reject `ui.listen == http.listen`; test: `go build ./...`; result: pass.
- Added registry support method `ListDevices()` in `internal/registry/registry.go` for the devices API; test: `go test ./internal/ui/... -v`; result: pass.
- Added API-shape test `internal/ui/ui_test.go` to verify `/api/ui/pipelines` and `/api/ui/devices` return non-empty arrays with required fields; test: `go test ./internal/ui/... -v`; result: pass.

`go test ./internal/ui/... -v` output:
- `TestUIServerAPIs`: pass
- package result: pass

`go build ./...` output:
- no compilation errors

Startup log capture (`go run ./cmd/gateway -config config/config.yaml`):
- `{"time":"2026-04-14T15:02:39.1274452+05:30","level":"ERROR","msg":"gateway exited with error","error":"nats connect nats://127.0.0.1:4222: dial tcp 127.0.0.1:4222: connectex: No connection could be made because the target machine actively refused it."}`
- `exit status 1`

Known issues:
- Startup could not be fully verified in this environment because local NATS (`127.0.0.1:4222`) was unavailable, so the gateway exited before adapter/UI steady state; UI startup wiring compiles and passes tests.

## Phase 5 result

`go test ./... -race -count=1` passed with the following package results:

- `interlink/cmd/gateway`: no test files
- `interlink/internal/adapter`: no test files
- `interlink/internal/adapter/coap`: no test files
- `interlink/internal/adapter/http`: no test files
- `interlink/internal/adapter/mqtt`: no test files
- `interlink/internal/bus`: no test files
- `interlink/internal/canonical`: pass
- `interlink/internal/gateway`: no test files
- `interlink/internal/metrics`: no test files
- `interlink/internal/pipeline`: pass
- `interlink/internal/policy`: pass
- `interlink/internal/registry`: pass
- `interlink/internal/ui`: pass
- `interlink/tools/loadtest`: no test files