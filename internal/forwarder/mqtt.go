package forwarder

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"text/template"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"

	"interlink/internal/bus"
	"interlink/internal/canonical"
)

type MQTTConfig struct {
	BrokerURL     string `yaml:"broker_url"`
	TopicTemplate string `yaml:"topic_template"`
	ClientID      string `yaml:"client_id"`
	QoS           byte   `yaml:"qos"`
}

type MQTTForwarder struct {
	cfg       MQTTConfig
	bus       bus.MessageBus
	client    pahomqtt.Client
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	sub       bus.Subscription
	msgCh     chan []byte
	msgMu     sync.RWMutex
	topicTmpl *template.Template
}

type mqttTopicData struct {
	DeviceID string
	Type     string
}

func NewMQTT(cfg MQTTConfig) (*MQTTForwarder, error) {
	if cfg.TopicTemplate == "" {
		cfg.TopicTemplate = "interlink/{{.DeviceID}}/{{.Type}}"
	}
	if cfg.ClientID == "" {
		cfg.ClientID = "interlink-forwarder-mqtt"
	}

	tmpl, err := template.New("topic").Parse(cfg.TopicTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse mqtt topic template: %w", err)
	}

	return &MQTTForwarder{
		cfg:       cfg,
		msgCh:     make(chan []byte, 2048),
		topicTmpl: tmpl,
	}, nil
}

func (f *MQTTForwarder) Name() string { return "mqtt" }

func (f *MQTTForwarder) Start(ctx context.Context, b bus.MessageBus) error {
	f.bus = b
	runCtx, cancel := context.WithCancel(ctx)
	f.cancel = cancel

	opts := pahomqtt.NewClientOptions().
		AddBroker(f.cfg.BrokerURL).
		SetClientID(f.cfg.ClientID).
		SetAutoReconnect(true).
		SetConnectRetry(true)

	f.client = pahomqtt.NewClient(opts)
	token := f.client.Connect()
	if !token.WaitTimeout(5 * time.Second) {
		return fmt.Errorf("mqtt forwarder connect timeout after 5s")
	}
	if err := token.Error(); err != nil {
		return fmt.Errorf("mqtt forwarder connect: %w", err)
	}

	sub, err := b.Subscribe("iot.>", func(subject string, data []byte) {
		_ = subject
		f.msgMu.RLock()
		ch := f.msgCh
		f.msgMu.RUnlock()
		if ch == nil {
			return
		}
		select {
		case ch <- append([]byte(nil), data...):
		default:
			slog.Warn("mqtt forwarder queue full; dropping message",
				"component", "forwarder/mqtt")
		}
	})
	if err != nil {
		return fmt.Errorf("mqtt forwarder subscribe iot.>: %w", err)
	}
	f.sub = sub

	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		f.run(runCtx)
	}()

	slog.Info("forwarder started",
		"component", "forwarder/mqtt",
		"broker_url", f.cfg.BrokerURL,
		"topic_template", f.cfg.TopicTemplate)
	return nil
}

func (f *MQTTForwarder) Stop(ctx context.Context) error {
	if f.cancel != nil {
		f.cancel()
	}
	if f.sub != nil {
		if err := f.sub.Unsubscribe(); err != nil {
			slog.Warn("mqtt forwarder unsubscribe failed",
				"component", "forwarder/mqtt",
				"error", err)
		}
	}

	f.msgMu.Lock()
	ch := f.msgCh
	f.msgCh = nil
	f.msgMu.Unlock()
	if ch != nil {
		close(ch)
	}

	done := make(chan struct{})
	go func() {
		f.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}

	if f.client != nil && f.client.IsConnected() {
		f.client.Disconnect(250)
	}
	slog.Info("forwarder stopped", "component", "forwarder/mqtt")
	return nil
}

func (f *MQTTForwarder) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-f.msgCh:
			if !ok {
				return
			}
			f.forward(data)
		}
	}
}

func (f *MQTTForwarder) forward(data []byte) {
	msg, err := canonical.UnmarshalMessage(data)
	if err != nil {
		slog.Warn("mqtt forwarder dropped invalid canonical message",
			"component", "forwarder/mqtt",
			"error", err)
		return
	}

	topic, err := f.renderTopic(msg)
	if err != nil {
		slog.Error("mqtt forwarder topic render failed",
			"component", "forwarder/mqtt",
			"device_id", msg.GetDeviceId(),
			"error", err)
		return
	}

	token := f.client.Publish(topic, f.cfg.QoS, false, data)
	token.Wait()
	if err := token.Error(); err != nil {
		slog.Error("mqtt forwarder publish failed",
			"component", "forwarder/mqtt",
			"topic", topic,
			"device_id", msg.GetDeviceId(),
			"type", canonical.SubjectType(msg),
			"error", err)
	}
}

func (f *MQTTForwarder) renderTopic(msg *canonical.Message) (string, error) {
	var buf bytes.Buffer
	err := f.topicTmpl.Execute(&buf, mqttTopicData{
		DeviceID: msg.GetDeviceId(),
		Type:     canonical.SubjectType(msg),
	})
	if err != nil {
		return "", err
	}
	if buf.Len() == 0 {
		return "", fmt.Errorf("rendered topic empty")
	}
	return buf.String(), nil
}
