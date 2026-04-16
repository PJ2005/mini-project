# SMOKE TESTS

## Prerequisites
- Gateway running:
  - `go run ./cmd/gateway -config config/config.yaml`
- NATS and registry active from gateway config.
- For WebSocket tests, set `websocket.listen` (example `:8090`).
- For AMQP tests, set `amqp.url`, `amqp.queue`, optional `exchange` and `routing_key`.
- For Modbus tests, set `modbus.host`, `modbus.port`, and `modbus.registers`.

## Shared verify command (latest value)
- `curl -s http://127.0.0.1:8080/api/v1/devices/<device_id>/latest | jq .`

## Modbus Adapter
### 1) Start simple Modbus TCP server (holding register 0 = 123)
- `python -m pip install pymodbus`
- `python -c "from pymodbus.server import StartTcpServer; from pymodbus.datastore import ModbusSequentialDataBlock, ModbusServerContext, ModbusSlaveContext; store=ModbusSlaveContext(hr=ModbusSequentialDataBlock(0,[123]*100), ir=ModbusSequentialDataBlock(0,[456]*100), co=ModbusSequentialDataBlock(0,[1]*100)); ctx=ModbusServerContext(slaves=store,single=True); StartTcpServer(ctx,address=('0.0.0.0',5020))"`

### 2) Configure gateway modbus section
- Example config snippet:
```yaml
modbus:
  host: "127.0.0.1"
  port: 5020
  poll_interval_ms: 1000
  registers:
    - name: "temp_raw"
      address: 0
      type: "holding"
      device_id: "mb-01"
```

### 3) Verify
- `curl -s http://127.0.0.1:8080/api/v1/devices/mb-01/latest | jq .`

## WebSocket Adapter
### 1) Connect and send telemetry via wscat
- `npm i -g wscat`
- `wscat -c ws://127.0.0.1:8090/ws/ws-01`
- In wscat prompt:
  - `{"metric":"temperature","value":24.7,"unit":"C"}`

### 2) Verify
- `curl -s http://127.0.0.1:8080/api/v1/devices/ws-01/latest | jq .`

## AMQP Adapter
### 1) Publish test message with curl (RabbitMQ management API)
- Requires management plugin on `:15672`.
- `curl -u guest:guest -H "content-type:application/json" -X POST http://127.0.0.1:15672/api/exchanges/%2F/amq.default/publish -d "{\"properties\":{},\"routing_key\":\"interlink.incoming\",\"payload\":\"{\\\"device_id\\\":\\\"amqp-01\\\",\\\"metric\\\":\\\"humidity\\\",\\\"value\\\":55.2,\\\"unit\\\":\\\"%\\\"}\",\"payload_encoding\":\"string\"}"`

### 2) Example AMQP gateway config
```yaml
amqp:
  url: "amqp://guest:guest@127.0.0.1:5672/"
  queue: "interlink.incoming"
  exchange: "amq.default"
  routing_key: "interlink.incoming"
  device_id_field: "device_id"
```

### 3) Verify
- `curl -s http://127.0.0.1:8080/api/v1/devices/amqp-01/latest | jq .`

