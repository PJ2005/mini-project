package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	gohttp "net/http"
	"strings"
	"sync"
	"time"

	"edgemesh/internal/bus"
	"edgemesh/internal/canonical"
	"edgemesh/internal/registry"
)

type Config struct {
	Listen string `yaml:"listen"`
}

type Adapter struct {
	cfg    Config
	bus    bus.MessageBus
	reg    *registry.Registry
	server *gohttp.Server

	mu     sync.RWMutex
	latest map[string][]byte // device_id → last serialized canonical Message

	ssesMu     sync.Mutex
	sseClients map[string][]chan []byte // device_id → list of SSE channels
}

func New(cfg Config) *Adapter {
	return &Adapter{
		cfg:        cfg,
		latest:     make(map[string][]byte),
		sseClients: make(map[string][]chan []byte),
	}
}

func (a *Adapter) Name() string { return "http" }

func (a *Adapter) Start(ctx context.Context, b bus.MessageBus, reg *registry.Registry) error {
	a.bus = b
	a.reg = reg

	// Subscribe to all telemetry to populate the in-memory cache.
	_, err := a.bus.Subscribe("iot.telemetry.>", func(subject string, data []byte) {
		deviceID := subjectDeviceID(subject)
		if deviceID == "" {
			return
		}
		a.mu.Lock()
		a.latest[deviceID] = data
		a.mu.Unlock()

		a.fanoutSSE(deviceID, data)
	})
	if err != nil {
		return fmt.Errorf("http subscribe telemetry: %w", err)
	}

	mux := gohttp.NewServeMux()
	mux.HandleFunc("POST /ingest/v1/{device_id}/telemetry", a.handleIngest)
	mux.HandleFunc("GET /api/v1/devices/{device_id}/latest", a.handleLatest)
	mux.HandleFunc("GET /api/v1/devices/{device_id}/stream", a.handleStream)
	mux.HandleFunc("POST /api/v1/devices/{device_id}/command", a.handleCommand)

	a.server = &gohttp.Server{
		Addr:    a.cfg.Listen,
		Handler: mux,
		BaseContext: func(_ net.Listener) context.Context {
			return ctx
		},
	}

	go func() {
		log.Printf("[http] listening on %s", a.cfg.Listen)
		if err := a.server.ListenAndServe(); err != nil && err != gohttp.ErrServerClosed {
			log.Printf("[http] serve error: %v", err)
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
	log.Println("[http] adapter stopped")
	return nil
}

// ── Ingest ──────────────────────────────────────────────

type ingestRequest struct {
	Metric string  `json:"metric"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
}

func (a *Adapter) handleIngest(w gohttp.ResponseWriter, r *gohttp.Request) {
	deviceID := r.PathValue("device_id")
	if deviceID == "" {
		gohttp.Error(w, `{"error":"missing device_id"}`, gohttp.StatusBadRequest)
		return
	}

	var req ingestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		gohttp.Error(w, `{"error":"invalid json"}`, gohttp.StatusBadRequest)
		return
	}

	msg := canonical.NewTelemetryMessage(deviceID, "http", req.Metric, req.Value, req.Unit)
	data, err := canonical.Marshal(msg)
	if err != nil {
		gohttp.Error(w, `{"error":"marshal failed"}`, gohttp.StatusInternalServerError)
		return
	}

	if err := a.bus.Publish(canonical.Subject(msg), data); err != nil {
		gohttp.Error(w, `{"error":"publish failed"}`, gohttp.StatusInternalServerError)
		return
	}

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

// ── Latest ──────────────────────────────────────────────

func (a *Adapter) handleLatest(w gohttp.ResponseWriter, r *gohttp.Request) {
	deviceID := r.PathValue("device_id")

	a.mu.RLock()
	data, ok := a.latest[deviceID]
	a.mu.RUnlock()

	if !ok {
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

// ── SSE Stream ──────────────────────────────────────────

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

	ch := make(chan []byte, 16)
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
}

func (a *Adapter) fanoutSSE(deviceID string, data []byte) {
	a.ssesMu.Lock()
	defer a.ssesMu.Unlock()
	for _, ch := range a.sseClients[deviceID] {
		select {
		case ch <- data:
		default:
			// Drop if consumer is slow — bounded channel prevents backpressure.
		}
	}
}

// ── Command ─────────────────────────────────────────────

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
	data, err := canonical.Marshal(msg)
	if err != nil {
		gohttp.Error(w, `{"error":"marshal failed"}`, gohttp.StatusInternalServerError)
		return
	}

	if err := a.bus.Publish(canonical.Subject(msg), data); err != nil {
		gohttp.Error(w, `{"error":"publish failed"}`, gohttp.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(gohttp.StatusAccepted)
	fmt.Fprintf(w, `{"message_id":"%s"}`, msg.GetMessageId())
}

// ── Helpers ─────────────────────────────────────────────

func subjectDeviceID(subject string) string {
	// iot.telemetry.<device_id>
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

// TimestampStr formats millisecond epoch to RFC3339 (unused but available for templates).
func TimestampStr(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}
