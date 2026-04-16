package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"interlink/internal/bus"
	"interlink/internal/canonical"
	"interlink/internal/registry"
)

type AMQPConfig struct {
	URL           string `yaml:"url"`
	Queue         string `yaml:"queue"`
	Exchange      string `yaml:"exchange"`
	RoutingKey    string `yaml:"routing_key"`
	DeviceIDField string `yaml:"device_id_field"`
}

type AMQPAdapter struct {
	cfg    AMQPConfig
	bus    bus.MessageBus
	reg    *registry.Registry
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewAMQP(cfg AMQPConfig) *AMQPAdapter {
	if cfg.DeviceIDField == "" {
		cfg.DeviceIDField = "device_id"
	}
	return &AMQPAdapter{cfg: cfg}
}

func (a *AMQPAdapter) Name() string { return "amqp" }

func (a *AMQPAdapter) Start(ctx context.Context, b bus.MessageBus, reg *registry.Registry) error {
	a.bus = b
	a.reg = reg
	runCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.consumeLoop(runCtx)
	}()

	slog.Info("adapter started",
		"component", "amqp",
		"queue", a.cfg.Queue,
		"exchange", a.cfg.Exchange,
		"routing_key", a.cfg.RoutingKey)
	return nil
}

func (a *AMQPAdapter) Stop(ctx context.Context) error {
	if a.cancel != nil {
		a.cancel()
	}

	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}

	slog.Info("adapter stopped", "component", "amqp")
	return nil
}

func (a *AMQPAdapter) consumeLoop(ctx context.Context) {
	backoff := 100 * time.Millisecond
	const maxBackoff = 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, ch, deliveries, err := a.connect()
		if err != nil {
			slog.Error("amqp connect/consume setup failed",
				"component", "amqp",
				"error", err,
				"retry_in", backoff.String())
			if !sleepWithContext(ctx, backoff) {
				return
			}
			backoff = minDuration(backoff*2, maxBackoff)
			continue
		}

		backoff = 100 * time.Millisecond
		slog.Info("amqp connected", "component", "amqp", "queue", a.cfg.Queue)

		connClosed := make(chan *amqp.Error, 1)
		chClosed := make(chan *amqp.Error, 1)
		conn.NotifyClose(connClosed)
		ch.NotifyClose(chClosed)

		reconnect := a.readDeliveries(ctx, deliveries, connClosed, chClosed)
		_ = ch.Close()
		_ = conn.Close()
		if !reconnect {
			return
		}
	}
}

func (a *AMQPAdapter) connect() (*amqp.Connection, *amqp.Channel, <-chan amqp.Delivery, error) {
	if a.cfg.URL == "" {
		return nil, nil, nil, fmt.Errorf("amqp url is empty")
	}
	if a.cfg.Queue == "" {
		return nil, nil, nil, fmt.Errorf("amqp queue is empty")
	}

	conn, err := amqp.Dial(a.cfg.URL)
	if err != nil {
		return nil, nil, nil, err
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, nil, nil, err
	}

	if _, err := ch.QueueDeclare(a.cfg.Queue, true, false, false, false, nil); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, nil, nil, err
	}
	if a.cfg.Exchange != "" {
		if err := ch.QueueBind(a.cfg.Queue, a.cfg.RoutingKey, a.cfg.Exchange, false, nil); err != nil {
			_ = ch.Close()
			_ = conn.Close()
			return nil, nil, nil, err
		}
	}

	deliveries, err := ch.Consume(a.cfg.Queue, "", true, false, false, false, nil)
	if err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, nil, nil, err
	}
	return conn, ch, deliveries, nil
}

func (a *AMQPAdapter) readDeliveries(
	ctx context.Context,
	deliveries <-chan amqp.Delivery,
	connClosed <-chan *amqp.Error,
	chClosed <-chan *amqp.Error,
) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case err := <-connClosed:
			if err != nil {
				slog.Warn("amqp connection closed", "component", "amqp", "error", err)
			}
			return true
		case err := <-chClosed:
			if err != nil {
				slog.Warn("amqp channel closed", "component", "amqp", "error", err)
			}
			return true
		case d, ok := <-deliveries:
			if !ok {
				slog.Warn("amqp delivery channel closed", "component", "amqp")
				return true
			}
			a.handleDelivery(d.Body)
		}
	}
}

func (a *AMQPAdapter) handleDelivery(body []byte) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		slog.Warn("amqp message ignored: invalid json", "component", "amqp", "error", err)
		return
	}

	deviceID, ok := asString(raw[a.cfg.DeviceIDField])
	if !ok || deviceID == "" {
		slog.Warn("amqp message ignored: missing device id field",
			"component", "amqp",
			"device_id_field", a.cfg.DeviceIDField)
		return
	}

	metric, ok := asString(raw["metric"])
	if !ok || metric == "" {
		slog.Warn("amqp message ignored: missing metric", "component", "amqp", "device_id", deviceID)
		return
	}

	value, ok := asFloat64(raw["value"])
	if !ok {
		slog.Warn("amqp message ignored: value must be numeric", "component", "amqp", "device_id", deviceID, "metric", metric)
		return
	}

	unit, _ := asString(raw["unit"])
	msg := canonical.NewTelemetryMessage(deviceID, "amqp", metric, value, unit)
	data, err := canonical.MarshalPooled(msg)
	if err != nil {
		slog.Error("amqp marshal failed", "component", "amqp", "device_id", deviceID, "error", err)
		return
	}
	if err := a.bus.Publish(canonical.Subject(msg), data); err != nil {
		slog.Error("amqp publish failed", "component", "amqp", "device_id", deviceID, "error", err)
		return
	}

	if err := a.reg.Register(registry.Device{
		DeviceID: deviceID,
		Name:     deviceID,
		Protocol: "amqp",
		Status:   "active",
	}); err != nil {
		slog.Error("amqp registry upsert failed", "component", "amqp", "device_id", deviceID, "error", err)
	}
}

func sleepWithContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func asString(v any) (string, bool) {
	switch val := v.(type) {
	case string:
		return val, true
	case fmt.Stringer:
		return val.String(), true
	default:
		return "", false
	}
}

func asFloat64(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int8:
		return float64(val), true
	case int16:
		return float64(val), true
	case int32:
		return float64(val), true
	case int64:
		return float64(val), true
	case uint:
		return float64(val), true
	case uint8:
		return float64(val), true
	case uint16:
		return float64(val), true
	case uint32:
		return float64(val), true
	case uint64:
		return float64(val), true
	case json.Number:
		f, err := val.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(val, 64)
		return f, err == nil
	default:
		return 0, false
	}
}
