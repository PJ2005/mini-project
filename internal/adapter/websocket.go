package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"interlink/internal/bus"
	"interlink/internal/canonical"
	"interlink/internal/metrics"
	"interlink/internal/registry"
)

type WebSocketConfig struct {
	Listen string `yaml:"listen"`
}

type WebSocketAdapter struct {
	cfg    WebSocketConfig
	bus    bus.MessageBus
	reg    *registry.Registry
	server *http.Server
	wg     sync.WaitGroup
}

type wsIngestPayload struct {
	Metric   string            `json:"metric"`
	Value    float64           `json:"value"`
	Unit     string            `json:"unit"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func NewWebSocket(cfg WebSocketConfig) *WebSocketAdapter {
	return &WebSocketAdapter{cfg: cfg}
}

func (a *WebSocketAdapter) Name() string { return "websocket" }

func (a *WebSocketAdapter) Start(ctx context.Context, b bus.MessageBus, reg *registry.Registry) error {
	a.bus = b
	a.reg = reg

	mux := http.NewServeMux()
	mux.Handle("/ws/", websocket.Handler(a.handleConn))

	a.server = &http.Server{
		Addr:    a.cfg.Listen,
		Handler: mux,
	}

	ln, err := net.Listen("tcp", a.cfg.Listen)
	if err != nil {
		return fmt.Errorf("websocket listen %s: %w", a.cfg.Listen, err)
	}

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		slog.Info("adapter listening", "component", "websocket", "listen", a.cfg.Listen)
		if err := a.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("websocket serve error", "component", "websocket", "error", err)
		}
	}()

	return nil
}

func (a *WebSocketAdapter) Stop(ctx context.Context) error {
	if a.server != nil {
		if err := a.server.Shutdown(ctx); err != nil {
			return fmt.Errorf("websocket shutdown: %w", err)
		}
	}
	a.wg.Wait()
	slog.Info("adapter stopped", "component", "websocket")
	return nil
}

func (a *WebSocketAdapter) handleConn(conn *websocket.Conn) {
	deviceID := strings.TrimPrefix(conn.Request().URL.Path, "/ws/")
	if deviceID == "" || strings.Contains(deviceID, "/") {
		slog.Warn("websocket connection rejected: invalid device id",
			"component", "websocket",
			"path", conn.Request().URL.Path)
		_ = conn.Close()
		return
	}

	for {
		var raw []byte
		if err := websocket.Message.Receive(conn, &raw); err != nil {
			return
		}

		var payload wsIngestPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			slog.Warn("websocket invalid json payload",
				"component", "websocket",
				"device_id", deviceID,
				"error", err)
			continue
		}
		if payload.Metric == "" {
			slog.Warn("websocket payload missing metric",
				"component", "websocket",
				"device_id", deviceID)
			continue
		}

		msg := canonical.NewTelemetryMessage(deviceID, "websocket", payload.Metric, payload.Value, payload.Unit)
		msg.Metadata = payload.Metadata
		data, err := canonical.MarshalPooled(msg)
		if err != nil {
			slog.Error("websocket marshal failed", "component", "websocket", "device_id", deviceID, "error", err)
			continue
		}
		publishStart := time.Now()
		if err := a.bus.Publish(canonical.Subject(msg), data); err != nil {
			slog.Error("websocket publish failed", "component", "websocket", "device_id", deviceID, "error", err)
			continue
		}
		metrics.ObserveMessageLatencyMS("websocket", "publish", time.Since(publishStart))

		if err := a.reg.Register(registry.Device{
			DeviceID: deviceID,
			Name:     deviceID,
			Protocol: "websocket",
			Status:   "active",
		}); err != nil {
			slog.Error("websocket registry upsert failed", "component", "websocket", "device_id", deviceID, "error", err)
		}
	}
}
