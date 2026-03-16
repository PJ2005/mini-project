# InterLink on Raspberry Pi 5 + ESP32 — Deployment Guide

A step-by-step guide to running InterLink on a Raspberry Pi 5 as the edge gateway, with an ESP32 microcontroller as the IoT sensor node. The ESP32 publishes telemetry over **MQTT** and **CoAP**; the RPi runs InterLink, NATS, and Mosquitto.

---

## Architecture

```
┌──────────────────────────┐          ┌──────────────────────────────────────────┐
│        ESP32             │          │          Raspberry Pi 5                  │
│                          │          │                                          │
│  DHT22 / BME280 sensor   │          │  ┌─────────┐  ┌──────┐  ┌────────────┐  │
│          │                │          │  │Mosquitto│  │ NATS │  │  InterLink  │  │
│  ┌───────▼───────┐       │   WiFi   │  │ :1883   │  │:4222 │  │  Gateway   │  │
│  │ MQTT publish  │───────┼──────────┼──►         │  │      │  │            │  │
│  │ CoAP POST     │───────┼──────────┼──┤         │  │      │  │  HTTP API  │  │
│  └───────────────┘       │          │  └────┬────┘  └──┬───┘  │  :8080     │  │
│                          │          │       └─────►────►│◄────┤  CoAP :5683│  │
│  Arduino / ESP-IDF       │          │                   │     └────────────┘  │
└──────────────────────────┘          └──────────────────────────────────────────┘
```

---

## Part 1 — Raspberry Pi 5 Setup

### 1.1 Flash Raspberry Pi OS

1. Download **Raspberry Pi Imager** from https://www.raspberrypi.com/software/
2. Flash **Raspberry Pi OS (64-bit, Lite)** to a microSD card (32 GB+ recommended).
3. In the imager settings (gear icon), enable:
   - **SSH** (password or key-based)
   - **WiFi** credentials (same network the ESP32 will join)
   - **Hostname** — e.g., `interlink-gw`
4. Insert the microSD, power on the RPi 5, and SSH in:

```bash
ssh pi@interlink-gw.local
```

### 1.2 System Updates

```bash
sudo apt update && sudo apt upgrade -y
sudo apt install -y git
```

> No C toolchain is required — InterLink uses the pure-Go `modernc.org/sqlite` driver.

### 1.3 Install Go

```bash
# Download Go for ARM64 (RPi 5 runs 64-bit ARM)
wget https://go.dev/dl/go1.24.1.linux-arm64.tar.gz
sudo tar -C /usr/local -xzf go1.24.1.linux-arm64.tar.gz

# Add to PATH (append to ~/.bashrc)
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc

# Verify
go version
# Expected: go version go1.24.1 linux/arm64
```

### 1.4 Install NATS Server

```bash
# Download NATS for ARM64
wget https://github.com/nats-io/nats-server/releases/download/v2.12.5/nats-server-v2.12.5-linux-arm64.tar.gz
tar -xzf nats-server-v2.12.5-linux-arm64.tar.gz
sudo mv nats-server-v2.12.5-linux-arm64/nats-server /usr/local/bin/

# Verify
nats-server --version
```

### 1.5 Install Mosquitto (MQTT Broker)

```bash
sudo apt install -y mosquitto mosquitto-clients

# Enable and start
sudo systemctl enable mosquitto
sudo systemctl start mosquitto

# Verify — should show "active (running)"
sudo systemctl status mosquitto
```

Edit `/etc/mosquitto/mosquitto.conf` to allow external connections (from ESP32):

```bash
sudo nano /etc/mosquitto/mosquitto.conf
```

Add these lines:

```
listener 1883
allow_anonymous true
```

Restart:

```bash
sudo systemctl restart mosquitto
```

---

## Part 2 — Build and Run InterLink on RPi 5

### 2.1 Clone and Build

```bash
git clone <your-repo-url> ~/interlink
cd ~/interlink

go mod tidy
go build -o interlink-gateway ./cmd/gateway
```

> Build takes ~30–60 seconds on RPi 5. The binary is ~30 MB.

### 2.2 Configure

Edit `config/config.yaml` — the defaults work out of the box:

```yaml
nats:
  url: "nats://127.0.0.1:4222"
  jetstream: false                # Enable JetStream for durable subs

mqtt:
  broker: "tcp://127.0.0.1:1883"
  client_id: "interlink-gw-01"
  topic: "devices/#"
  qos: 1
  device_id_topic_index: 1

http:
  listen: ":8080"
  sse_channel_capacity: 256       # SSE channel buffer per client
  command_timeout: "5s"           # Command ack timeout

coap:
  listen: ":5683"

registry:
  db_path: "./interlink.db"

heartbeat_timeout: "5m"            # Devices not seen → inactive

policy:
  default_action: "allow"
  rules:
    - device_pattern: "*"
      source_proto: "*"
      message_type: "telemetry"
      action: "allow"
    - device_pattern: "*"
      source_proto: "*"
      message_type: "command"
      action: "allow"
    - device_pattern: "*"
      source_proto: "*"
      message_type: "event"
      action: "allow"
```

### 2.3 Start Everything

Open **three terminal sessions** (or use `tmux`):

**Terminal 1 — NATS:**
```bash
nats-server
```

**Terminal 2 — InterLink:**
```bash
cd ~/interlink
./interlink-gateway -config config/config.yaml
```

You should see:

```json
{"level":"INFO","msg":"connected to NATS","component":"gateway","url":"nats://127.0.0.1:4222"}
{"level":"INFO","msg":"registry opened","component":"gateway","db_path":"./interlink.db"}
{"level":"INFO","msg":"policy engine loaded","component":"gateway","rules":3,"default_action":"allow"}
{"level":"INFO","msg":"adapter started","component":"mqtt","broker":"tcp://127.0.0.1:1883"}
{"level":"INFO","msg":"listening","component":"http","listen":":8080"}
{"level":"INFO","msg":"listening","component":"coap","listen":":5683","transport":"UDP"}
{"level":"INFO","msg":"InterLink is running","component":"gateway"}
```

> All logs are structured JSON via `log/slog`, suitable for log aggregation tools.

### 2.4 Quick Smoke Test (on the RPi itself)

```bash
# Publish via MQTT
mosquitto_pub -t "devices/test-device" -m '{"temperature": 22.5}'

# Check via HTTP API
curl http://localhost:8080/api/v1/devices/test-device/latest
```

If you get a JSON response with the temperature data, the gateway is working.

### 2.5 Note the RPi's IP Address

```bash
hostname -I
# Example: 192.168.1.100
```

You'll use this IP in the ESP32 firmware.

---

## Part 3 — ESP32 Setup (Arduino IDE)

### 3.1 Prerequisites

1. Install **Arduino IDE 2.x** from https://www.arduino.cc/en/software
2. Add the ESP32 board manager URL:
   - Go to **File → Preferences → Additional Board Manager URLs**
   - Add: `https://espressif.github.io/arduino-esp32/package_esp32_index.json`
3. Install the **ESP32** board package:
   - **Tools → Board → Boards Manager** → search "esp32" → Install
4. Install required libraries via **Sketch → Include Library → Manage Libraries**:
   - `PubSubClient` (by Nick O'Leary) — for MQTT
   - `WiFi` (built-in with ESP32 board package)

### 3.2 MQTT Telemetry Sketch

Create a new sketch and paste the following. Update the WiFi and RPi IP:

```cpp
// ── InterLink ESP32 MQTT Sensor Node ──────────────────────
//
// Reads a simulated temperature value and publishes it to
// the InterLink gateway over MQTT every 5 seconds.
// For a real sensor, replace the random value with DHT22/BME280 readings.

#include <WiFi.h>
#include <PubSubClient.h>

// ── Configuration ────────────────────────────────────────
const char* WIFI_SSID     = "YOUR_WIFI_SSID";
const char* WIFI_PASSWORD = "YOUR_WIFI_PASSWORD";
const char* MQTT_BROKER   = "192.168.1.100";  // ← RPi 5 IP address
const int   MQTT_PORT     = 1883;
const char* DEVICE_ID     = "esp32-sensor-01";

// MQTT topic: devices/<device_id>
// InterLink extracts device_id from topic index 1 (after "devices/")
String mqttTopic = String("devices/") + DEVICE_ID;

WiFiClient   wifiClient;
PubSubClient mqttClient(wifiClient);

// ── WiFi Connection ──────────────────────────────────────
void connectWiFi() {
  Serial.printf("Connecting to WiFi: %s", WIFI_SSID);
  WiFi.begin(WIFI_SSID, WIFI_PASSWORD);
  while (WiFi.status() != WL_CONNECTED) {
    delay(500);
    Serial.print(".");
  }
  Serial.printf("\nWiFi connected. IP: %s\n", WiFi.localIP().toString().c_str());
}

// ── MQTT Connection ──────────────────────────────────────
void connectMQTT() {
  mqttClient.setServer(MQTT_BROKER, MQTT_PORT);
  while (!mqttClient.connected()) {
    Serial.printf("Connecting to MQTT broker %s:%d...\n", MQTT_BROKER, MQTT_PORT);
    if (mqttClient.connect(DEVICE_ID)) {
      Serial.println("MQTT connected.");
    } else {
      Serial.printf("MQTT connect failed (rc=%d). Retrying in 2s...\n", mqttClient.state());
      delay(2000);
    }
  }
}

// ── Setup ────────────────────────────────────────────────
void setup() {
  Serial.begin(115200);
  delay(1000);

  connectWiFi();
  connectMQTT();
}

// ── Loop ─────────────────────────────────────────────────
void loop() {
  if (!mqttClient.connected()) {
    connectMQTT();
  }
  mqttClient.loop();

  // Simulated temperature reading (replace with real sensor).
  // For DHT22: float temp = dht.readTemperature();
  float temperature = 20.0 + random(0, 100) / 10.0;  // 20.0 – 29.9 °C

  // InterLink accepts flat or nested JSON.
  // The MQTT adapter picks the first numeric key-value pair.
  char payload[64];
  snprintf(payload, sizeof(payload), "{\"temperature\": %.1f}", temperature);

  // Publish to MQTT
  if (mqttClient.publish(mqttTopic.c_str(), payload)) {
    Serial.printf("Published to %s: %s\n", mqttTopic.c_str(), payload);
  } else {
    Serial.println("Publish failed!");
  }

  delay(5000);  // Send every 5 seconds
}
```

### 3.3 Upload to ESP32

1. Connect the ESP32 via USB.
2. Select your board: **Tools → Board → ESP32 Dev Module** (or your specific variant).
3. Select the correct port: **Tools → Port → COMx** (Windows) or `/dev/ttyUSB0` (Linux).
4. Click **Upload** (→ button).
5. Open **Serial Monitor** (115200 baud) to see output:

```
Connecting to WiFi: MyNetwork
WiFi connected. IP: 192.168.1.105
Connecting to MQTT broker 192.168.1.100:1883...
MQTT connected.
Published to devices/esp32-sensor-01: {"temperature": 24.3}
Published to devices/esp32-sensor-01: {"temperature": 21.7}
```

---

## Part 4 — ESP32 with CoAP (Optional)

If you want to test the CoAP adapter as well, use the **ESP-IDF** framework which includes a native CoAP client. Alternatively, use MicroPython with the `microcoapy` library.

### 4.1 MicroPython + CoAP

1. Flash MicroPython onto the ESP32: https://micropython.org/download/esp32/
2. Install `microcoapy`:

```python
# In the MicroPython REPL:
import mip
mip.install("github:insighio/microCoAPy")
```

3. Create `main.py` on the ESP32:

```python
import network
import time
import json
import microcoapy

# ── WiFi ──────────────────────────────────────────────────
WIFI_SSID     = "YOUR_WIFI_SSID"
WIFI_PASSWORD = "YOUR_WIFI_PASSWORD"

wlan = network.WLAN(network.STA_IF)
wlan.active(True)
wlan.connect(WIFI_SSID, WIFI_PASSWORD)
while not wlan.isconnected():
    time.sleep(0.5)
print("WiFi connected:", wlan.ifconfig()[0])

# ── CoAP Client ──────────────────────────────────────────
RPI_IP    = "192.168.1.100"  # ← RPi 5 IP address
COAP_PORT = 5683
DEVICE_ID = "esp32-coap-01"

client = microcoapy.Coap()
client.start()

while True:
    # Simulated humidity reading
    import random
    humidity = 40.0 + random.uniform(0, 30)

    payload = json.dumps({
        "metric": "humidity",
        "value": round(humidity, 1),
        "unit": "%"
    })

    # POST to /telemetry/<device_id>
    client.post(
        RPI_IP,
        COAP_PORT,
        "telemetry/" + DEVICE_ID,
        payload
    )

    print(f"CoAP POST → {RPI_IP}:{COAP_PORT}/telemetry/{DEVICE_ID}: {payload}")
    time.sleep(5)
```

---

## Part 5 — Verifying the Full Pipeline

### 5.1 Check InterLink Logs (RPi terminal)

You should see structured JSON log lines for each message received.

You can also check the `/health` endpoint:

```bash
curl http://192.168.1.100:8080/health | python3 -m json.tool
```

Expected response (enhanced with performance data):
```json
{
  "uptime_seconds": 120,
  "nats_connected": true,
  "device_count": 2,
  "adapters": ["mqtt", "http", "coap"],
  "memory": {
    "heap_alloc_mb": 4.2,
    "heap_inuse_mb": 6.0,
    "stack_inuse_mb": 0.5,
    "sys_mb": 12.1,
    "gc_pause_ms": 0.12,
    "gc_runs": 8
  },
  "runtime": {
    "goroutines": 18,
    "go_version": "go1.24.1"
  },
  "storage": {
    "db_size_mb": 0.1,
    "dead_letters": 0,
    "latest_messages": 2
  },
  "throughput": {
    "mqtt_received": 24,
    "mqtt_published": 24,
    "total_published": 24
  }
}
```

### 5.2 Query the HTTP API

From any machine on the same network (or on the RPi itself):

```bash
# Latest telemetry from the MQTT sensor
curl http://192.168.1.100:8080/api/v1/devices/esp32-sensor-01/latest

# Latest telemetry from the CoAP sensor
curl http://192.168.1.100:8080/api/v1/devices/esp32-coap-01/latest
```

Expected response:

```json
{
  "message_id": "a1b2c3d4...",
  "device_id": "esp32-sensor-01",
  "timestamp_ms": 1741654200000,
  "source_proto": "mqtt",
  "type": "telemetry",
  "payload": {
    "metric": "temperature",
    "value": 24.3,
    "unit": ""
  }
}
```

### 5.3 Real-Time Streaming

Open a browser or terminal and stream SSE:

```bash
curl -N http://192.168.1.100:8080/api/v1/devices/esp32-sensor-01/stream
```

You will see live `data:` events every 5 seconds as the ESP32 publishes.

### 5.4 Send a Command to the ESP32

```bash
curl -X POST http://192.168.1.100:8080/api/v1/devices/esp32-sensor-01/command \
  -H "Content-Type: application/json" \
  -d '{"action":"set_interval","params":{"seconds":10}}'
```

> **Note:** The ESP32 sketch above does not subscribe to commands. To receive commands, add an MQTT subscription to `commands/esp32-sensor-01` on the ESP32 and add a NATS-to-MQTT command relay in InterLink (future enhancement).

### 5.5 Monitor Performance

InterLink tracks ~20 performance parameters continuously. From any machine on the network:

```bash
# Memory, storage, throughput, and runtime info
curl http://192.168.1.100:8080/health | python3 -m json.tool

# Prometheus metrics (for Grafana, alerting, etc.)
curl http://192.168.1.100:8080/metrics
```

Key metrics to watch on the RPi 5:

| Metric | What to Watch | Healthy Range |
|---|---|---|
| `memory.heap_alloc_mb` | Go heap allocation | < 50 MB |
| `memory.sys_mb` | Total OS memory used | < 100 MB |
| `runtime.goroutines` | Active goroutines | 15–30 (idle) |
| `storage.db_size_mb` | SQLite file size | Grows slowly |
| `storage.dead_letters` | Failed publishes | Should be 0 |
| `throughput.total_published` | Messages processed | Increases steadily |

---

## Part 6 — Running as a systemd Service (Production)

To keep InterLink running across reboots:

### 6.1 Create Service Files

```bash
# NATS service
sudo tee /etc/systemd/system/nats.service > /dev/null <<'EOF'
[Unit]
Description=NATS Server
After=network.target

[Service]
ExecStart=/usr/local/bin/nats-server
Restart=always
User=pi

[Install]
WantedBy=multi-user.target
EOF

# InterLink service
sudo tee /etc/systemd/system/interlink.service > /dev/null <<'EOF'
[Unit]
Description=InterLink IoT Gateway
After=network.target nats.service mosquitto.service
Requires=nats.service mosquitto.service

[Service]
ExecStart=/home/pi/interlink/interlink-gateway -config /home/pi/interlink/config/config.yaml
WorkingDirectory=/home/pi/interlink
Restart=always
User=pi

[Install]
WantedBy=multi-user.target
EOF
```

### 6.2 Enable and Start

```bash
sudo systemctl daemon-reload
sudo systemctl enable nats mosquitto interlink
sudo systemctl start nats interlink
```

### 6.3 Check Status

```bash
sudo systemctl status interlink
# Should show "active (running)"

journalctl -u interlink -f
# Follow live logs
```

---

## Part 7 — Using a Real Sensor (DHT22 Example)

Replace the simulated temperature in the Arduino sketch with a real DHT22 reading.

### 7.1 Wiring

| DHT22 Pin | ESP32 Pin |
|-----------|-----------|
| VCC (1)   | 3.3V      |
| Data (2)  | GPIO 4    |
| GND (4)   | GND       |

Place a 10kΩ pull-up resistor between VCC and Data.

### 7.2 Code Changes

Install the `DHT sensor library` by Adafruit via Library Manager, then update the sketch:

```cpp
#include <DHT.h>

#define DHT_PIN  4
#define DHT_TYPE DHT22
DHT dht(DHT_PIN, DHT_TYPE);

void setup() {
  // ... existing WiFi/MQTT setup ...
  dht.begin();
}

void loop() {
  // ... existing MQTT reconnect logic ...

  float temperature = dht.readTemperature();
  float humidity    = dht.readHumidity();

  if (isnan(temperature) || isnan(humidity)) {
    Serial.println("DHT read failed, skipping.");
    delay(2000);
    return;
  }

  // Publish temperature
  char tempPayload[64];
  snprintf(tempPayload, sizeof(tempPayload), "{\"temperature\": %.1f}", temperature);
  mqttClient.publish(mqttTopic.c_str(), tempPayload);

  // Publish humidity as a separate message
  char humPayload[64];
  snprintf(humPayload, sizeof(humPayload), "{\"humidity\": %.1f}", humidity);
  mqttClient.publish(mqttTopic.c_str(), humPayload);

  Serial.printf("Published: temp=%.1f°C, humidity=%.1f%%\n", temperature, humidity);
  delay(5000);
}
```

---

## Troubleshooting

| Symptom | Fix |
|---|---|
| ESP32 can't connect to MQTT | Check RPi IP, confirm Mosquitto allows anonymous (`allow_anonymous true`), check firewall (`sudo ufw allow 1883`) |
| `mqtt connect: timed out after 5s` | Mosquitto not running or wrong broker address in `config.yaml` |
| `nats: no servers available` | NATS server not started — run `nats-server` first |
| CoAP messages not arriving | Check UDP port 5683 is open (`sudo ufw allow 5683/udp`) |
| `go build` fails on RPi | Ensure Go 1.22+ is installed and `GOPATH` is set correctly |
| High CPU on RPi | Reduce ESP32 publish rate (increase `delay()` in sketch) |
| `permission denied` on binary | Run `chmod +x interlink-gateway` |

---

## Summary

| Component | Role | Port |
|---|---|---|
| **ESP32** | Sensor node — publishes MQTT/CoAP telemetry | WiFi client |
| **Mosquitto** (RPi) | MQTT broker — receives ESP32 messages | `1883` |
| **NATS** (RPi) | Internal message bus | `4222` |
| **InterLink** (RPi) | Gateway — adapters, registry, policy, API | HTTP `:8080`, CoAP `:5683` |

**Data flow:** ESP32 → WiFi → Mosquitto (MQTT) / InterLink (CoAP) → NATS → HTTP API → dashboards/consumers.

**Monitoring:** `GET /health` (JSON) for quick checks, `GET /metrics` (Prometheus) for dashboards and alerting.
