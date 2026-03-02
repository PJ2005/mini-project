package mqtt

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

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
			log.Printf("[mqtt] connected to %s, subscribing to %s", a.cfg.Broker, a.cfg.Topic)
			c.Subscribe(a.cfg.Topic, a.cfg.QoS, a.onMessage)
		}).
		SetConnectionLostHandler(func(_ pahomqtt.Client, err error) {
			log.Printf("[mqtt] connection lost: %v", err)
		})

	a.client = pahomqtt.NewClient(opts)
	token := a.client.Connect()
	token.Wait()
	if err := token.Error(); err != nil {
		return fmt.Errorf("mqtt connect %s: %w", a.cfg.Broker, err)
	}

	// Monitor context cancellation for clean shutdown.
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		<-ctx.Done()
	}()

	log.Printf("[mqtt] adapter started (broker=%s topic=%s)", a.cfg.Broker, a.cfg.Topic)
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
	log.Println("[mqtt] adapter stopped")
	return nil
}

func (a *Adapter) onMessage(_ pahomqtt.Client, msg pahomqtt.Message) {
	deviceID := a.extractDeviceID(msg.Topic())
	if deviceID == "" {
		log.Printf("[mqtt] cannot extract device_id from topic %s", msg.Topic())
		return
	}

	m, err := a.convertPayload(deviceID, msg.Payload())
	if err != nil {
		log.Printf("[mqtt] convert error for %s: %v", deviceID, err)
		return
	}

	data, err := canonical.Marshal(m)
	if err != nil {
		log.Printf("[mqtt] marshal error for %s: %v", deviceID, err)
		return
	}

	if err := a.bus.Publish(canonical.Subject(m), data); err != nil {
		log.Printf("[mqtt] publish error for %s: %v", deviceID, err)
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
		return ""
	}
	return parts[idx]
}

// convertPayload turns a flat JSON object {"key": number} into the first
// key-value pair as a TelemetryPayload. Nested objects are ignored.
func (a *Adapter) convertPayload(deviceID string, raw []byte) (*canonical.Message, error) {
	var flat map[string]any
	if err := json.Unmarshal(raw, &flat); err != nil {
		return nil, fmt.Errorf("json decode: %w", err)
	}

	for key, val := range flat {
		num, ok := toFloat64(val)
		if !ok {
			continue
		}
		return canonical.NewTelemetryMessage(deviceID, "mqtt", key, num, ""), nil
	}
	return nil, fmt.Errorf("no numeric key-value pair in payload")
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
