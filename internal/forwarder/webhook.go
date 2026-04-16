package forwarder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"interlink/internal/bus"
	"interlink/internal/canonical"
)

type WebhookConfig struct {
	URL     string            `yaml:"url"`
	Method  string            `yaml:"method"`
	Headers map[string]string `yaml:"headers"`
	Filter  string            `yaml:"filter"`
}

type WebhookForwarder struct {
	cfg    WebhookConfig
	bus    bus.MessageBus
	sub    bus.Subscription
	client *http.Client
	msgCh  chan []byte
	msgMu  sync.RWMutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewWebhook(cfg WebhookConfig) *WebhookForwarder {
	method := strings.ToUpper(cfg.Method)
	if method != http.MethodPut {
		method = http.MethodPost
	}
	cfg.Method = method
	return &WebhookForwarder{
		cfg:    cfg,
		client: &http.Client{Timeout: 5 * time.Second},
		msgCh:  make(chan []byte, 2048),
	}
}

func (f *WebhookForwarder) Name() string { return "webhook" }

func (f *WebhookForwarder) Start(ctx context.Context, b bus.MessageBus) error {
	f.bus = b
	runCtx, cancel := context.WithCancel(ctx)
	f.cancel = cancel

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
			slog.Warn("webhook forwarder queue full; dropping message",
				"component", "forwarder/webhook")
		}
	})
	if err != nil {
		return fmt.Errorf("webhook subscribe iot.>: %w", err)
	}
	f.sub = sub

	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		f.run(runCtx)
	}()

	slog.Info("forwarder started",
		"component", "forwarder/webhook",
		"url", f.cfg.URL,
		"method", f.cfg.Method,
		"filter", f.cfg.Filter)
	return nil
}

func (f *WebhookForwarder) Stop(ctx context.Context) error {
	if f.cancel != nil {
		f.cancel()
	}
	if f.sub != nil {
		if err := f.sub.Unsubscribe(); err != nil {
			slog.Warn("webhook forwarder unsubscribe failed",
				"component", "forwarder/webhook",
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

	slog.Info("forwarder stopped", "component", "forwarder/webhook")
	return nil
}

func (f *WebhookForwarder) run(ctx context.Context) {
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

func (f *WebhookForwarder) forward(data []byte) {
	msg, err := canonical.UnmarshalMessage(data)
	if err != nil {
		slog.Warn("webhook forwarder dropped invalid canonical message",
			"component", "forwarder/webhook",
			"error", err)
		return
	}

	if !f.matchesFilter(msg.GetDeviceId()) {
		return
	}

	payload, err := json.Marshal(webhookJSON(msg))
	if err != nil {
		slog.Error("webhook marshal failed",
			"component", "forwarder/webhook",
			"device_id", msg.GetDeviceId(),
			"error", err)
		return
	}

	req, err := http.NewRequest(f.cfg.Method, f.cfg.URL, bytes.NewReader(payload))
	if err != nil {
		slog.Error("webhook request create failed",
			"component", "forwarder/webhook",
			"device_id", msg.GetDeviceId(),
			"error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range f.cfg.Headers {
		req.Header.Set(k, v)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		slog.Error("webhook request failed",
			"component", "forwarder/webhook",
			"device_id", msg.GetDeviceId(),
			"url", f.cfg.URL,
			"error", err)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Error("webhook non-success response",
			"component", "forwarder/webhook",
			"device_id", msg.GetDeviceId(),
			"url", f.cfg.URL,
			"status_code", resp.StatusCode)
	}
}

func (f *WebhookForwarder) matchesFilter(deviceID string) bool {
	if f.cfg.Filter == "" {
		return true
	}
	ok, err := filepath.Match(f.cfg.Filter, deviceID)
	if err != nil {
		slog.Warn("webhook forwarder filter parse failed; dropping message",
			"component", "forwarder/webhook",
			"filter", f.cfg.Filter,
			"device_id", deviceID,
			"error", err)
		return false
	}
	return ok
}

type webhookMessage struct {
	MessageID   string            `json:"message_id"`
	DeviceID    string            `json:"device_id"`
	TimestampMs int64             `json:"timestamp_ms"`
	SourceProto string            `json:"source_proto"`
	Type        string            `json:"type"`
	Payload     map[string]any    `json:"payload"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

func webhookJSON(msg *canonical.Message) webhookMessage {
	out := webhookMessage{
		MessageID:   msg.GetMessageId(),
		DeviceID:    msg.GetDeviceId(),
		TimestampMs: msg.GetTimestampMs(),
		SourceProto: msg.GetSourceProto(),
		Type:        canonical.SubjectType(msg),
		Metadata:    msg.GetMetadata(),
		Payload:     map[string]any{},
	}

	switch p := msg.Payload.(type) {
	case *canonical.Message_Telemetry:
		out.Payload["metric"] = p.Telemetry.GetMetric()
		out.Payload["value"] = p.Telemetry.GetValue()
		out.Payload["unit"] = p.Telemetry.GetUnit()
	case *canonical.Message_Command:
		out.Payload["action"] = p.Command.GetAction()
		out.Payload["params"] = json.RawMessage(p.Command.GetParams())
	case *canonical.Message_Event:
		out.Payload["event_type"] = p.Event.GetEventType()
		out.Payload["severity"] = p.Event.GetSeverity()
		out.Payload["detail"] = p.Event.GetDetail()
	}

	return out
}
