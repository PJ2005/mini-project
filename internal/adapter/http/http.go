package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	gohttp "net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"interlink/internal/bus"
	"interlink/internal/canonical"
	"interlink/internal/metrics"
	"interlink/internal/policy"
	"interlink/internal/registry"
)

type Config struct {
	Listen         string        `yaml:"listen"`
	SSEChannelCap  int           `yaml:"sse_channel_capacity"`
	CommandTimeout time.Duration `yaml:"command_timeout"`
}

type Adapter struct {
	cfg    Config
	bus    bus.MessageBus
	reg    *registry.Registry
	server *gohttp.Server
	engine *policy.Engine

	adapterNames []string
	startTime    time.Time

	ssesMu     sync.RWMutex
	sseClients map[string][]chan []byte

	sseClientCount atomic.Int64
}

func New(cfg Config) *Adapter {
	if cfg.SSEChannelCap <= 0 {
		cfg.SSEChannelCap = 256
	}
	if cfg.CommandTimeout <= 0 {
		cfg.CommandTimeout = 5 * time.Second
	}
	return &Adapter{
		cfg:        cfg,
		sseClients: make(map[string][]chan []byte),
	}
}

func (a *Adapter) Name() string { return "http" }

func (a *Adapter) SetAdapterNames(names []string)   { a.adapterNames = names }
func (a *Adapter) SetPolicyEngine(e *policy.Engine) { a.engine = e }

func (a *Adapter) Start(ctx context.Context, b bus.MessageBus, reg *registry.Registry) error {
	a.bus = b
	a.reg = reg
	a.startTime = time.Now()

	mux := gohttp.NewServeMux()
	mux.HandleFunc("POST /ingest/v1/{device_id}/telemetry", a.handleIngest)
	mux.HandleFunc("GET /api/v1/devices/{device_id}/latest", a.handleLatest)
	mux.HandleFunc("GET /api/v1/devices/{device_id}/stream", a.handleStream)
	mux.HandleFunc("POST /api/v1/devices/{device_id}/command", a.handleCommand)
	mux.HandleFunc("GET /health", a.handleHealth)
	mux.Handle("GET /metrics", promhttp.Handler())

	// Wrap with latency-tracking middleware
	handler := a.metricsMiddleware(mux)

	// Subscribe to telemetry for latest-message cache + SSE fanout.
	_, err := b.Subscribe("iot.telemetry.>", func(subject string, data []byte) {
		deviceID := subjectDeviceID(subject)
		if deviceID == "" {
			return
		}
		if err := reg.UpsertLatestMessage(deviceID, data); err != nil {
			slog.Error("upsert latest message", "component", "http", "device_id", deviceID, "error", err)
		}
		a.broadcastToSSEClients(deviceID, data)
	})
	if err != nil {
		return fmt.Errorf("http subscribe iot.telemetry.>: %w", err)
	}

	a.server = &gohttp.Server{
		Addr:    a.cfg.Listen,
		Handler: handler,
	}

	ln, err := net.Listen("tcp", a.cfg.Listen)
	if err != nil {
		return fmt.Errorf("http listen %s: %w", a.cfg.Listen, err)
	}

	go func() {
		slog.Info("listening", "component", "http", "listen", a.cfg.Listen)
		if err := a.server.Serve(ln); err != nil && !errors.Is(err, gohttp.ErrServerClosed) {
			slog.Error("http serve error", "component", "http", "error", err)
		}
	}()

	return nil
}

func (a *Adapter) Stop(ctx context.Context) error {
	if a.server != nil {
		if err := a.server.Shutdown(ctx); err != nil {
			return fmt.Errorf("http shutdown: %w", err)
		}
	}
	slog.Info("adapter stopped", "component", "http")
	return nil
}

// ── HTTP Latency Middleware ─────────────────────────────

func (a *Adapter) metricsMiddleware(next gohttp.Handler) gohttp.Handler {
	return gohttp.HandlerFunc(func(w gohttp.ResponseWriter, r *gohttp.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		// Normalize path for label cardinality control
		path := normalizePath(r.URL.Path)
		metrics.HTTPRequestDuration.WithLabelValues(r.Method, path).Observe(time.Since(start).Seconds())
	})
}

func normalizePath(path string) string {
	switch {
	case strings.HasPrefix(path, "/ingest/"):
		return "/ingest/v1/{device_id}/telemetry"
	case strings.HasSuffix(path, "/latest"):
		return "/api/v1/devices/{device_id}/latest"
	case strings.HasSuffix(path, "/stream"):
		return "/api/v1/devices/{device_id}/stream"
	case strings.HasSuffix(path, "/command"):
		return "/api/v1/devices/{device_id}/command"
	case path == "/health":
		return "/health"
	case path == "/metrics":
		return "/metrics"
	default:
		return path
	}
}

// ── Ingest ──────────────────────────────────────────────

type ingestRequest struct {
	Metric string  `json:"metric"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
}

func (a *Adapter) handleIngest(w gohttp.ResponseWriter, r *gohttp.Request) {
	totalStart := time.Now()
	defer func() {
		if rv := recover(); rv != nil {
			slog.Error("panic recovered in ingest handler", "component", "http", "error", fmt.Sprintf("%v", rv))
			gohttp.Error(w, `{"error":"internal error"}`, gohttp.StatusInternalServerError)
		}
	}()

	metrics.RecordReceive("http")

	deviceID := r.PathValue("device_id")
	if deviceID == "" {
		gohttp.Error(w, `{"error":"missing device_id"}`, gohttp.StatusBadRequest)
		return
	}

	r.Body = gohttp.MaxBytesReader(w, r.Body, 1<<20)

	var req ingestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *gohttp.MaxBytesError
		if errors.As(err, &maxErr) {
			gohttp.Error(w, `{"error":"request body too large"}`, gohttp.StatusRequestEntityTooLarge)
			return
		}
		gohttp.Error(w, `{"error":"invalid json"}`, gohttp.StatusBadRequest)
		return
	}

	marshalStart := time.Now()
	msg := canonical.NewTelemetryMessage(deviceID, "http", req.Metric, req.Value, req.Unit)
	data, err := canonical.MarshalPooled(msg)
	if err != nil {
		gohttp.Error(w, `{"error":"marshal failed"}`, gohttp.StatusInternalServerError)
		return
	}
	metrics.MessageProcessingDuration.WithLabelValues("http", "marshal").Observe(time.Since(marshalStart).Seconds())

	publishStart := time.Now()
	if err := a.bus.Publish(canonical.Subject(msg), data); err != nil {
		gohttp.Error(w, `{"error":"publish failed"}`, gohttp.StatusInternalServerError)
		return
	}
	metrics.MessageProcessingDuration.WithLabelValues("http", "publish").Observe(time.Since(publishStart).Seconds())
	metrics.RecordPublish("http")

	metrics.MessageProcessingDuration.WithLabelValues("http", "total").Observe(time.Since(totalStart).Seconds())

	a.reg.Register(registry.Device{
		DeviceID: deviceID,
		Name:     deviceID,
		Protocol: "http",
		Status:   "active",
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(gohttp.StatusAccepted)
	fmt.Fprintf(w, `{"message_id":"%s"}`, msg.GetMessageId())
}

// ── Latest ─────────────────────────────────────────────

func (a *Adapter) handleLatest(w gohttp.ResponseWriter, r *gohttp.Request) {
	deviceID := r.PathValue("device_id")

	data, err := a.reg.GetLatestMessage(deviceID)
	if err != nil {
		gohttp.Error(w, `{"error":"database error"}`, gohttp.StatusInternalServerError)
		return
	}
	if data == nil {
		gohttp.Error(w, `{"error":"no data for device"}`, gohttp.StatusNotFound)
		return
	}

	msg, err := canonical.UnmarshalMessage(data)
	if err != nil {
		gohttp.Error(w, `{"error":"unmarshal failed"}`, gohttp.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(messageToJSON(msg))
}

// ── SSE Stream ─────────────────────────────────────────

func (a *Adapter) handleStream(w gohttp.ResponseWriter, r *gohttp.Request) {
	deviceID := r.PathValue("device_id")

	flusher, ok := w.(gohttp.Flusher)
	if !ok {
		gohttp.Error(w, `{"error":"streaming not supported"}`, gohttp.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan []byte, a.cfg.SSEChannelCap)
	a.addSSEClient(deviceID, ch)
	defer a.removeSSEClient(deviceID, ch)

	for {
		select {
		case <-r.Context().Done():
			return
		case data := <-ch:
			msg, err := canonical.UnmarshalMessage(data)
			if err != nil {
				continue
			}
			payload, _ := json.Marshal(messageToJSON(msg))
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

func (a *Adapter) addSSEClient(deviceID string, ch chan []byte) {
	a.ssesMu.Lock()
	defer a.ssesMu.Unlock()
	a.sseClients[deviceID] = append(a.sseClients[deviceID], ch)
	a.sseClientCount.Add(1)
	metrics.SSEClientsActive.Set(float64(a.sseClientCount.Load()))
}

func (a *Adapter) removeSSEClient(deviceID string, ch chan []byte) {
	a.ssesMu.Lock()
	defer a.ssesMu.Unlock()
	clients := a.sseClients[deviceID]
	for i, c := range clients {
		if c == ch {
			a.sseClients[deviceID] = append(clients[:i], clients[i+1:]...)
			break
		}
	}
	close(ch)
	a.sseClientCount.Add(-1)
	metrics.SSEClientsActive.Set(float64(a.sseClientCount.Load()))
}

func (a *Adapter) broadcastToSSEClients(deviceID string, data []byte) {
	a.ssesMu.RLock()
	clients := append([]chan []byte(nil), a.sseClients[deviceID]...)
	a.ssesMu.RUnlock()

	for _, ch := range clients {
		select {
		case ch <- data:
		default:
			slog.Warn("sse: slow consumer, dropping message",
				"component", "http", "device_id", deviceID)
			metrics.SSEDroppedMessagesTotal.Inc()
		}
	}
}

// ── Command ────────────────────────────────────────────

type commandRequest struct {
	Action string          `json:"action"`
	Params json.RawMessage `json:"params"`
}

func (a *Adapter) handleCommand(w gohttp.ResponseWriter, r *gohttp.Request) {
	deviceID := r.PathValue("device_id")
	if deviceID == "" {
		gohttp.Error(w, `{"error":"missing device_id"}`, gohttp.StatusBadRequest)
		return
	}

	var req commandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		gohttp.Error(w, `{"error":"invalid json"}`, gohttp.StatusBadRequest)
		return
	}

	msg := canonical.NewCommandMessage(deviceID, "http", req.Action, req.Params)
	data, err := canonical.MarshalPooled(msg)
	if err != nil {
		gohttp.Error(w, `{"error":"marshal failed"}`, gohttp.StatusInternalServerError)
		return
	}

	subject := canonical.Subject(msg)
	replyData, err := a.bus.Request(subject, data, a.cfg.CommandTimeout)

	w.Header().Set("Content-Type", "application/json")

	if err != nil {
		ackStatus := "error"
		if strings.Contains(err.Error(), "timeout") {
			ackStatus = "timeout"
		}
		w.WriteHeader(gohttp.StatusAccepted)
		fmt.Fprintf(w, `{"message_id":"%s","ack_status":"%s","error":"%s"}`,
			msg.GetMessageId(), ackStatus, err.Error())
		return
	}

	w.WriteHeader(gohttp.StatusOK)
	fmt.Fprintf(w, `{"message_id":"%s","ack_status":"ok","ack_data":%s}`,
		msg.GetMessageId(), string(replyData))
}

// ── Health (Enhanced) ───────────────────────────────────

func (a *Adapter) handleHealth(w gohttp.ResponseWriter, r *gohttp.Request) {
	uptime := time.Since(a.startTime).Seconds()
	natsConnected := a.bus.IsConnected()
	deviceCount := 0
	if a.reg != nil {
		deviceCount, _ = a.reg.DeviceCount()
	}

	resp := map[string]any{
		"uptime_seconds": int(uptime),
		"nats_connected": natsConnected,
		"device_count":   deviceCount,
		"adapters":       a.adapterNames,
	}

	// Memory & runtime stats
	runtimeSnap := metrics.RuntimeSnapshot()
	for k, v := range runtimeSnap {
		resp[k] = v
	}

	// Storage stats
	if a.reg != nil {
		storage := metrics.StorageSnapshot(a.reg)
		if lmc, err := a.reg.LatestMessageCount(); err == nil {
			storage["latest_messages"] = lmc
		}
		resp["storage"] = storage
	}

	// Throughput stats
	resp["throughput"] = metrics.ThroughputSnapshot()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ── Registry Gauge Refresh ─────────────────────────────

func (a *Adapter) StartGaugeRefresh(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.refreshGaugeMetrics()
			}
		}
	}()
}

func (a *Adapter) refreshGaugeMetrics() {
	if a.reg != nil {
		if counts, err := a.reg.DeviceCountByStatus(); err == nil {
			for key, cnt := range counts {
				parts := strings.SplitN(key, "/", 2)
				if len(parts) == 2 {
					metrics.RegistryDevices.WithLabelValues(parts[0], parts[1]).Set(float64(cnt))
				}
			}
		}
	}
}

// ── Helpers ─────────────────────────────────────────────

func subjectDeviceID(subject string) string {
	parts := strings.Split(subject, ".")
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

type jsonMessage struct {
	MessageID   string            `json:"message_id"`
	DeviceID    string            `json:"device_id"`
	TimestampMs int64             `json:"timestamp_ms"`
	SourceProto string            `json:"source_proto"`
	Type        string            `json:"type"`
	Payload     any               `json:"payload"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

func messageToJSON(m *canonical.Message) jsonMessage {
	j := jsonMessage{
		MessageID:   m.GetMessageId(),
		DeviceID:    m.GetDeviceId(),
		TimestampMs: m.GetTimestampMs(),
		SourceProto: m.GetSourceProto(),
		Type:        canonical.SubjectType(m),
		Metadata:    m.GetMetadata(),
	}

	switch p := m.Payload.(type) {
	case *canonical.Message_Telemetry:
		j.Payload = map[string]any{
			"metric": p.Telemetry.GetMetric(),
			"value":  p.Telemetry.GetValue(),
			"unit":   p.Telemetry.GetUnit(),
		}
	case *canonical.Message_Command:
		j.Payload = map[string]any{
			"action": p.Command.GetAction(),
			"params": json.RawMessage(p.Command.GetParams()),
		}
	case *canonical.Message_Event:
		j.Payload = map[string]any{
			"event_type": p.Event.GetEventType(),
			"severity":   p.Event.GetSeverity(),
			"detail":     p.Event.GetDetail(),
		}
	}

	return j
}

func TimestampStr(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}
