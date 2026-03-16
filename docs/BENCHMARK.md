# InterLink vs Node-RED — Benchmark Guide

This guide walks you through running a fair, reproducible benchmark comparing **InterLink** (Go) against **Node-RED** (Node.js) on the same Raspberry Pi 5.

---

## Prerequisites

On the RPi 5, ensure these are running:
```bash
nats-server &        # port 4222
mosquitto &          # port 1883
```

---

## Part 1 — Set Up Node-RED

### 1.1 Install

```bash
sudo npm install -g --unsafe-perm node-red
```

### 1.2 Import the Comparison Flow

```bash
# Copy the flow file to the RPi
scp tools/nodered-flow.json pi@<RPI_IP>:~/nodered-flow.json
```

### 1.3 Start Node-RED

```bash
node-red --port 1880 &
```

### 1.4 Import the Flow

1. Open `http://<RPI_IP>:1880` in a browser.
2. Click the hamburger menu (☰) → **Import** → **Clipboard**.
3. Paste the contents of `tools/nodered-flow.json`.
4. Click **Import** → **Deploy**.

Node-RED now exposes:
- `GET http://<RPI_IP>:1880/health` — health + memory stats
- `GET http://<RPI_IP>:1880/api/v1/devices/:id/latest` — latest message
- `POST http://<RPI_IP>:1880/ingest/v1/:id/telemetry` — HTTP ingest

And subscribes to `devices/#` on the local Mosquitto broker — same as InterLink.

### 1.5 Verify

```bash
# Send a test message
mosquitto_pub -t "devices/test-01" -m '{"temperature": 22.5}'

# Check Node-RED received it
curl http://localhost:1880/api/v1/devices/test-01/latest
curl http://localhost:1880/health
```

---

## Part 2 — Build the Load Test Tool

On your development machine (or the RPi):

```bash
cd interlink
go build -o loadtest ./tools/loadtest/
```

Copy to RPi if built elsewhere:
```bash
scp loadtest pi@<RPI_IP>:~/loadtest
```

---

## Part 3 — Run Benchmarks

> **Important:** Run each test separately. Stop one system before starting the other. Reboot between tests for a clean state.

### 3.1 Benchmark InterLink

```bash
# 1. Make sure InterLink is running
./interlink-gateway -config config/config.yaml &

# 2. Wait 10 seconds for it to stabilize
sleep 10

# 3. Run 1000 messages at 100 msg/s
./loadtest -broker tcp://127.0.0.1:1883 \
           -messages 1000 \
           -rate 100 \
           -concurrency 4 \
           -health http://127.0.0.1:8080/health

# 4. Run 5000 messages at 500 msg/s (stress test)
./loadtest -broker tcp://127.0.0.1:1883 \
           -messages 5000 \
           -rate 500 \
           -concurrency 4 \
           -health http://127.0.0.1:8080/health

# 5. Run unlimited rate (throughput ceiling)
./loadtest -broker tcp://127.0.0.1:1883 \
           -messages 5000 \
           -rate 0 \
           -concurrency 4 \
           -health http://127.0.0.1:8080/health
```

Each run saves a JSON file like `benchmark_20260311_213000_1000msg.json`.

### 3.2 Benchmark Node-RED

```bash
# 1. Stop InterLink, reboot for clean state
sudo reboot

# 2. After reboot, start infrastructure + Node-RED
nats-server &
mosquitto &
node-red --port 1880 &

# 3. Wait 10 seconds
sleep 10

# 4. Same tests, pointing health at Node-RED
./loadtest -broker tcp://127.0.0.1:1883 \
           -messages 1000 \
           -rate 100 \
           -concurrency 4 \
           -health http://127.0.0.1:1880/health

./loadtest -broker tcp://127.0.0.1:1883 \
           -messages 5000 \
           -rate 500 \
           -concurrency 4 \
           -health http://127.0.0.1:1880/health

./loadtest -broker tcp://127.0.0.1:1883 \
           -messages 5000 \
           -rate 0 \
           -concurrency 4 \
           -health http://127.0.0.1:1880/health
```

---

## Part 4 — Collect Additional Metrics

While each test runs, capture these in a **separate terminal**:

### 4.1 CPU & Memory (during test)

```bash
# Record CPU and memory every 1 second for 30 seconds
pidstat -p $(pgrep -f interlink-gateway) 1 30 > interlink_cpu.txt
# OR for Node-RED:
pidstat -p $(pgrep -f node-red) 1 30 > nodered_cpu.txt
```

If `pidstat` is not available:
```bash
# Simple alternative
top -b -n 30 -d 1 -p $(pgrep -f interlink-gateway) > interlink_cpu.txt
```

### 4.2 Idle Memory Baseline

Before running the load test, capture idle memory:
```bash
# InterLink idle
curl -s http://127.0.0.1:8080/health | python3 -m json.tool > interlink_idle.json

# Node-RED idle
curl -s http://127.0.0.1:1880/health | python3 -m json.tool > nodered_idle.json
```

### 4.3 Binary / Install Size

```bash
# InterLink
ls -lh interlink-gateway
# → ~25-30 MB single binary

# Node-RED
du -sh $(which node-red)
du -sh ~/.node-red/
npm list -g --depth=0 2>/dev/null | head -5
# → typically 100-200 MB with dependencies
```

### 4.4 Cold Start Time

```bash
# InterLink
time ./interlink-gateway -config config/config.yaml &
# → typically < 1 second

# Node-RED
time node-red --port 1880 &
# → typically 3-8 seconds
```

---

## Part 5 — Build the Comparison Table

After all tests complete, fill in this table:

### 5.1 At a Glance

| Metric | InterLink | Node-RED |
|---|---|---|
| Language | Go 1.26 (compiled) | Node.js (V8 JIT) |
| Binary size | __ MB | __ MB |
| Cold start time | __ s | __ s |
| Dependencies | NATS + Mosquitto | Mosquitto only |

### 5.2 Idle State

| Metric | InterLink | Node-RED |
|---|---|---|
| RSS memory | __ MB | __ MB |
| Heap allocated | __ MB | __ MB |
| CPU (%) | __ % | __ % |
| Goroutines/Threads | __ | __ |

### 5.3 Load Test — 1,000 messages @ 100 msg/s

| Metric | InterLink | Node-RED |
|---|---|---|
| Throughput (actual) | __ msg/s | __ msg/s |
| Latency p50 | __ ms | __ ms |
| Latency p95 | __ ms | __ ms |
| Latency p99 | __ ms | __ ms |
| Errors | __ | __ |
| RSS after | __ MB | __ MB |
| CPU peak (%) | __ % | __ % |

### 5.4 Stress Test — 5,000 messages @ 500 msg/s

| Metric | InterLink | Node-RED |
|---|---|---|
| Throughput (actual) | __ msg/s | __ msg/s |
| Latency p50 | __ ms | __ ms |
| Latency p95 | __ ms | __ ms |
| Latency p99 | __ ms | __ ms |
| Errors | __ | __ |
| RSS after | __ MB | __ MB |

### 5.5 Throughput Ceiling (unlimited rate)

| Metric | InterLink | Node-RED |
|---|---|---|
| Max throughput | __ msg/s | __ msg/s |
| Latency p50 at max | __ ms | __ ms |
| Latency p99 at max | __ ms | __ ms |

### 5.6 Feature Comparison

| Feature | InterLink | Node-RED |
|---|---|---|
| Protocol bridging (MQTT/HTTP/CoAP) | ✅ Built-in | ⚠️ Requires extra nodes |
| Canonical data model (Protobuf) | ✅ | ❌ Raw JSON |
| Policy engine (allow/deny/routing) | ✅ Hot-reloadable | ❌ Manual flow editing |
| Device registry (SQLite) | ✅ Persistent | ❌ In-memory only |
| Dead letter queue | ✅ | ❌ |
| Heartbeat timeout | ✅ Auto-inactive | ❌ |
| Prometheus metrics | ✅ 20+ metrics | ⚠️ Requires node-red-contrib |
| SSE streaming | ✅ Built-in | ⚠️ Requires extra nodes |
| Single binary deployment | ✅ | ❌ Requires Node.js runtime |

---

## Part 6 — Reading the JSON Report

Each load test outputs a JSON file like:

```json
{
  "config": {
    "total_messages": 1000,
    "target_rate_per_sec": 100,
    "concurrency": 4,
    "broker": "tcp://127.0.0.1:1883"
  },
  "throughput": {
    "total_sent": 1000,
    "total_errors": 0,
    "duration_seconds": 10.05,
    "actual_rate_per_sec": 99.50
  },
  "latency": {
    "min_ms": 0.045,
    "max_ms": 2.310,
    "mean_ms": 0.120,
    "p50_ms": 0.098,
    "p95_ms": 0.250,
    "p99_ms": 0.890
  },
  "health_after": { ... },
  "errors": 0
}
```

**Key fields:**
- `throughput.actual_rate_per_sec` — if this is lower than `target_rate_per_sec`, the system is saturated
- `latency.p99_ms` — worst-case latency experienced by 99% of messages
- `errors` — should be 0; non-zero means the system is dropping messages
- `health_after.memory` — memory usage after the load test completed

---

## Tips

- **Always reboot between tests** to ensure a clean state.
- **Run each test 3 times** and take the median for reliable results.
- **Same hardware, same network** — both systems must run on the exact same RPi 5.
- **Same message format** — the load test uses the same JSON payloads for both.
- **Node-RED doesn't use NATS** — it processes messages in-process. This is fair because InterLink's NATS overhead is part of its architecture, and the comparison shows what you get for that overhead (protocol bridging, policy, persistence).
