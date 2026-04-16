package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"

	"interlink/internal/bus"
	"interlink/internal/canonical"
	"interlink/internal/metrics"
	"interlink/internal/registry"
)

type Config struct {
	Broker             string `yaml:"broker"`
	ClientID           string `yaml:"client_id"`
	Topic              string `yaml:"topic"`
	QoS                byte   `yaml:"qos"`
	DeviceIDTopicIndex int    `yaml:"device_id_topic_index"`
}

type Adapter struct {
	cfg    Config
	client pahomqtt.Client
	bus    bus.MessageBus
	reg    *registry.Registry
	wg     sync.WaitGroup
	cancel context.CancelFunc

	msgMu           sync.RWMutex
	msgChan         chan mqttMsg
	droppedMessages atomic.Int64
}

type mqttMsg struct {
	deviceID string
	raw      []byte
}

func New(cfg Config) *Adapter {
	return &Adapter{cfg: cfg}
}

func (a *Adapter) Name() string { return "mqtt" }

func (a *Adapter) Start(ctx context.Context, b bus.MessageBus, reg *registry.Registry) error {
	a.bus = b
	a.reg = reg
	_, a.cancel = context.WithCancel(ctx)
	a.msgMu.Lock()
	a.msgChan = make(chan mqttMsg, 1024)
	a.msgMu.Unlock()

	opts := pahomqtt.NewClientOptions().
		AddBroker(a.cfg.Broker).
		SetClientID(a.cfg.ClientID).
		SetAutoReconnect(true).
		SetOnConnectHandler(func(c pahomqtt.Client) {
			slog.Info("connected, subscribing",
				"component", "mqtt",
				"broker", a.cfg.Broker,
				"topic", a.cfg.Topic)
			c.Subscribe(a.cfg.Topic, a.cfg.QoS, a.onMessage)
		}).
		SetConnectionLostHandler(func(_ pahomqtt.Client, err error) {
			slog.Warn("connection lost", "component", "mqtt", "error", err)
		})

	a.client = pahomqtt.NewClient(opts)
	token := a.client.Connect()
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("mqtt connect %s: timed out after 5s", a.cfg.Broker)
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("mqtt connect %s: %w", a.cfg.Broker, err)
	}

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.runMessagePump()
	}()

	slog.Info("adapter started",
		"component", "mqtt",
		"broker", a.cfg.Broker,
		"topic", a.cfg.Topic)
	return nil
}

func (a *Adapter) Stop(ctx context.Context) error {
	if a.cancel != nil {
		a.cancel()
	}
	if a.client != nil && a.client.IsConnected() {
		a.client.Disconnect(250)
	}
	a.msgMu.Lock()
	ch := a.msgChan
	a.msgChan = nil
	a.msgMu.Unlock()
	if ch != nil {
		close(ch)
	}
	a.wg.Wait()
	slog.Info("adapter stopped", "component", "mqtt")
	return nil
}

func (a *Adapter) onMessage(_ pahomqtt.Client, msg pahomqtt.Message) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic recovered in message handler", "component", "mqtt", "error", fmt.Sprintf("%v", r))
		}
	}()

	metrics.RecordReceive("mqtt")

	deviceID := a.extractDeviceID(msg.Topic())
	if deviceID == "" {
		return
	}

	a.msgMu.RLock()
	ch := a.msgChan
	if ch == nil {
		a.msgMu.RUnlock()
		return
	}
	select {
	case ch <- mqttMsg{deviceID: deviceID, raw: append([]byte(nil), msg.Payload()...)}:
		a.msgMu.RUnlock()
	default:
		dropped := a.droppedMessages.Add(1)
		a.msgMu.RUnlock()
		slog.Warn("mqtt: ingestion buffer full, dropping message",
			"component", "mqtt",
			"device_id", deviceID,
			"dropped_messages", dropped)
	}
}

func (a *Adapter) runMessagePump() {
	a.msgMu.RLock()
	ch := a.msgChan
	a.msgMu.RUnlock()
	if ch == nil {
		return
	}

	for msg := range ch {
		a.processMessage(msg)
	}
}

func (a *Adapter) processMessage(msg mqttMsg) {
	totalStart := time.Now()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic recovered in MQTT worker", "component", "mqtt", "error", fmt.Sprintf("%v", r))
		}
		metrics.MessageProcessingDuration.WithLabelValues("mqtt", "total").Observe(time.Since(totalStart).Seconds())
	}()

	convertStart := time.Now()
	m, err := a.convertPayload(msg.deviceID, msg.raw)
	if err != nil {
		slog.Warn("convert error", "component", "mqtt", "device_id", msg.deviceID, "error", err)
		return
	}
	metrics.MessageProcessingDuration.WithLabelValues("mqtt", "convert").Observe(time.Since(convertStart).Seconds())

	marshalStart := time.Now()
	data, err := canonical.MarshalPooled(m)
	if err != nil {
		slog.Error("marshal error", "component", "mqtt", "device_id", msg.deviceID, "error", err)
		return
	}
	metrics.MessageProcessingDuration.WithLabelValues("mqtt", "marshal").Observe(time.Since(marshalStart).Seconds())

	publishStart := time.Now()
	if err := a.bus.Publish(canonical.Subject(m), data); err != nil {
		slog.Error("publish error", "component", "mqtt", "device_id", msg.deviceID, "error", err)
		return
	}
	metrics.ObserveMessageLatencyMS("mqtt", "publish", time.Since(publishStart))
	metrics.MessageProcessingDuration.WithLabelValues("mqtt", "publish").Observe(time.Since(publishStart).Seconds())
	metrics.RecordPublish("mqtt")

	if err := a.reg.Register(registry.Device{
		DeviceID: msg.deviceID,
		Name:     msg.deviceID,
		Protocol: "mqtt",
		Status:   "active",
	}); err != nil {
		slog.Error("registry upsert error", "component", "mqtt", "device_id", msg.deviceID, "error", err)
		return
	}

	slog.Info("telemetry received",
		"component", "mqtt",
		"device_id", msg.deviceID,
		"latency_ms", time.Since(totalStart).Milliseconds())
}

func (a *Adapter) extractDeviceID(topic string) string {
	parts := strings.Split(topic, "/")
	idx := a.cfg.DeviceIDTopicIndex
	if idx < 0 || idx >= len(parts) {
		slog.Error("topic has fewer segments than device_id_topic_index",
			"component", "mqtt",
			"topic", topic,
			"segments", len(parts),
			"device_id_topic_index", idx)
		return ""
	}
	id := parts[idx]
	if id == "" {
		slog.Error("empty device_id at topic index",
			"component", "mqtt",
			"topic", topic,
			"device_id_topic_index", idx)
		return ""
	}
	return id
}

func (a *Adapter) convertPayload(deviceID string, raw []byte) (*canonical.Message, error) {
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("payload is not valid JSON: %w (raw=%q)", err, string(raw))
	}
	metadata := extractStringMetadata(top["metadata"])

	for key, val := range top {
		if num, ok := toFloat64(val); ok {
			msg := canonical.NewTelemetryMessage(deviceID, "mqtt", key, num, "")
			msg.Metadata = metadata
			return msg, nil
		}
	}

	for outerKey, val := range top {
		nested, ok := val.(map[string]any)
		if !ok {
			continue
		}
		for innerKey, innerVal := range nested {
			if num, ok := toFloat64(innerVal); ok {
				metric := outerKey + "." + innerKey
				msg := canonical.NewTelemetryMessage(deviceID, "mqtt", metric, num, "")
				msg.Metadata = metadata
				return msg, nil
			}
		}
	}

	return nil, fmt.Errorf("no numeric value found in payload (tried top-level and one level nested)")
}

func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}

func extractStringMetadata(v any) map[string]string {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string)
	for k, raw := range obj {
		switch val := raw.(type) {
		case string:
			out[k] = val
		case fmt.Stringer:
			out[k] = val.String()
		default:
			out[k] = fmt.Sprintf("%v", val)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
