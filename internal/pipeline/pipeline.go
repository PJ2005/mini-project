package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"interlink/internal/bus"
	"interlink/internal/canonical"
	"interlink/internal/registry"
)

type Pipeline struct {
	cfg      PipelineConfig
	bus      bus.MessageBus
	reg      *registry.Registry
	subject  string
	client   *http.Client
	sub      bus.Subscription
	msgCh    chan sourceMessage
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once
}

type sourceMessage struct {
	subject string
	data    []byte
}

func New(cfg PipelineConfig, b bus.MessageBus, reg *registry.Registry) (*Pipeline, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("pipeline name is required")
	}

	subject, err := sourceSubject(cfg.Source)
	if err != nil {
		return nil, err
	}

	if err := validateSink(cfg.Sink); err != nil {
		return nil, err
	}

	timeout := cfg.Sink.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	return &Pipeline{
		cfg:     cfg,
		bus:     b,
		reg:     reg,
		subject: subject,
		client:  &http.Client{Timeout: timeout},
		msgCh:   make(chan sourceMessage, 1024),
	}, nil
}

func (p *Pipeline) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel

	sub, err := p.bus.Subscribe(p.subject, func(subject string, data []byte) {
		select {
		case p.msgCh <- sourceMessage{subject: subject, data: append([]byte(nil), data...)}:
		default:
			slog.Warn("pipeline: source buffer full, dropping message",
				"component", "pipeline",
				"pipeline", p.cfg.Name,
				"subject", subject)
		}
	})
	if err != nil {
		cancel()
		return fmt.Errorf("pipeline subscribe %s: %w", p.subject, err)
	}
	p.sub = sub

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.run(runCtx)
	}()

	slog.Info("pipeline started",
		"component", "pipeline",
		"pipeline", p.cfg.Name,
		"source", p.cfg.Source.Type,
		"sink", p.cfg.Sink.Type,
		"subject", p.subject)
	return nil
}

func (p *Pipeline) Stop(ctx context.Context) {
	p.stopOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
		if p.sub != nil {
			if err := p.sub.Unsubscribe(); err != nil {
				slog.Warn("pipeline unsubscribe failed",
					"component", "pipeline",
					"pipeline", p.cfg.Name,
					"error", err)
			}
		}
		done := make(chan struct{})
		go func() {
			p.wg.Wait()
			close(done)
		}()
		select {
		case <-ctx.Done():
		case <-done:
		}
	})
}

func (p *Pipeline) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-p.msgCh:
			p.handleMessage(msg)
		}
	}
}

func (p *Pipeline) handleMessage(msg sourceMessage) {
	canon, err := canonical.UnmarshalMessage(msg.data)
	if err != nil {
		slog.Error("pipeline unmarshal failed",
			"component", "pipeline",
			"pipeline", p.cfg.Name,
			"subject", msg.subject,
			"error", err)
		return
	}

	tel := canon.GetTelemetry()
	if tel == nil {
		return
	}

	record := Record{tel.GetMetric(): tel.GetValue()}
	transformed, ok := RunChain(p.cfg.Transforms, record)
	if !ok {
		return
	}

	metric, value, ok := resolveOutput(transformed, tel.GetMetric())
	if !ok {
		return
	}

	if err := p.dispatch(canon.GetDeviceId(), metric, value, tel.GetUnit()); err != nil {
		slog.Error("pipeline dispatch failed",
			"component", "pipeline",
			"pipeline", p.cfg.Name,
			"sink", p.cfg.Sink.Type,
			"error", err)
	}
}

func (p *Pipeline) dispatch(deviceID, metric string, value float64, unit string) error {
	switch p.cfg.Sink.Type {
	case "http_post":
		payload := map[string]any{
			"device_id": deviceID,
			"metric":    metric,
			"value":     value,
			"unit":      unit,
		}
		body, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal http payload: %w", err)
		}
		req, err := http.NewRequest(http.MethodPost, p.cfg.Sink.URL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("create http request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := p.client.Do(req)
		if err != nil {
			return fmt.Errorf("http post: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("http post status: %s", resp.Status)
		}
		return nil

	case "nats_publish":
		out := canonical.NewTelemetryMessage(deviceID, "pipeline", metric, value, unit)
		data, err := canonical.MarshalPooled(out)
		if err != nil {
			return fmt.Errorf("marshal nats output: %w", err)
		}
		if err := p.bus.Publish(p.cfg.Sink.Subject, data); err != nil {
			return fmt.Errorf("nats publish: %w", err)
		}
		return nil

	case "mqtt_publish":
		payload := map[string]any{
			"device_id": deviceID,
			"metric":    metric,
			"value":     value,
			"unit":      unit,
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal mqtt output: %w", err)
		}
		if err := p.bus.Publish(p.cfg.Sink.Topic, data); err != nil {
			return fmt.Errorf("mqtt publish: %w", err)
		}
		return nil

	default:
		return fmt.Errorf("unsupported sink type: %s", p.cfg.Sink.Type)
	}
}

func sourceSubject(src SourceConfig) (string, error) {
	switch src.Type {
	case "nats":
		if src.Subject == "" {
			return "", fmt.Errorf("nats source requires subject")
		}
		return src.Subject, nil
	case "mqtt":
		if src.Topic == "" {
			return "", fmt.Errorf("mqtt source requires topic")
		}
		s := strings.ReplaceAll(src.Topic, "/", ".")
		s = strings.ReplaceAll(s, "#", ">")
		s = strings.ReplaceAll(s, "+", "*")
		return s, nil
	default:
		return "", fmt.Errorf("unsupported source type: %s", src.Type)
	}
}

func validateSink(s SinkConfig) error {
	switch s.Type {
	case "http_post":
		if s.URL == "" {
			return fmt.Errorf("http_post sink requires url")
		}
	case "nats_publish":
		if s.Subject == "" {
			return fmt.Errorf("nats_publish sink requires subject")
		}
	case "mqtt_publish":
		if s.Topic == "" {
			return fmt.Errorf("mqtt_publish sink requires topic")
		}
	default:
		return fmt.Errorf("unsupported sink type: %s", s.Type)
	}
	return nil
}

func resolveOutput(r Record, defaultMetric string) (string, float64, bool) {
	if v, ok := r["value"]; ok {
		return defaultMetric, v, true
	}
	if v, ok := r[defaultMetric]; ok {
		return defaultMetric, v, true
	}
	for k, v := range r {
		return k, v, true
	}
	return "", 0, false
}
