package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"

	"edgemesh/internal/bus"
	"edgemesh/internal/canonical"
	"edgemesh/internal/registry"
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
}

func New(cfg Config) *Adapter {
	return &Adapter{cfg: cfg}
}

func (a *Adapter) Name() string { return "mqtt" }

func (a *Adapter) Start(ctx context.Context, b bus.MessageBus, reg *registry.Registry) error {
	a.bus = b
	a.reg = reg
	ctx, a.cancel = context.WithCancel(ctx)

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
		<-ctx.Done()
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

	deviceID := a.extractDeviceID(msg.Topic())
	if deviceID == "" {
		return
	}

	m, err := a.convertPayload(deviceID, msg.Payload())
	if err != nil {
		slog.Warn("convert error", "component", "mqtt", "device_id", deviceID, "error", err)
		return
	}

	data, err := canonical.Marshal(m)
	if err != nil {
		slog.Error("marshal error", "component", "mqtt", "device_id", deviceID, "error", err)
		return
	}

	if err := a.bus.Publish(canonical.Subject(m), data); err != nil {
		slog.Error("publish error", "component", "mqtt", "device_id", deviceID, "error", err)
		return
	}

	a.reg.Register(registry.Device{
		DeviceID: deviceID,
		Name:     deviceID,
		Protocol: "mqtt",
		Status:   "active",
	})
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

	for key, val := range top {
		if num, ok := toFloat64(val); ok {
			return canonical.NewTelemetryMessage(deviceID, "mqtt", key, num, ""), nil
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
				return canonical.NewTelemetryMessage(deviceID, "mqtt", metric, num, ""), nil
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
