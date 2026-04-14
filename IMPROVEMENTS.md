# IMPROVEMENTS

Single source of truth for changes made in this project.

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