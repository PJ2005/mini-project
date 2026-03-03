package coap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	coap "github.com/plgd-dev/go-coap/v3"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"

	"edgemesh/internal/bus"
	"edgemesh/internal/canonical"
	"edgemesh/internal/registry"
)

// Config holds CoAP adapter settings from config.yaml.
type Config struct {
	Listen string `yaml:"listen"`
}

// Adapter implements the adapter.Adapter interface for CoAP over UDP.
// It receives telemetry and event messages from constrained IoT devices
// using the Constrained Application Protocol (RFC 7252).
type Adapter struct {
	cfg    Config
	bus    bus.MessageBus
	reg    *registry.Registry
	wg     sync.WaitGroup
	cancel context.CancelFunc
}

// New creates a CoAP adapter with the given configuration.
func New(cfg Config) *Adapter {
	return &Adapter{cfg: cfg}
}

func (a *Adapter) Name() string { return "coap" }

// Start begins listening for CoAP requests on the configured UDP address.
// Two path prefixes are handled:
//
//	/telemetry/<device_id> — ingest telemetry data
//	/event/<device_id>     — ingest event data
func (a *Adapter) Start(ctx context.Context, b bus.MessageBus, reg *registry.Registry) error {
	a.bus = b
	a.reg = reg
	_, a.cancel = context.WithCancel(ctx)

	router := mux.NewRouter()
	router.Handle("/telemetry/{device_id}", mux.HandlerFunc(a.handleTelemetry))
	router.Handle("/event/{device_id}", mux.HandlerFunc(a.handleEvent))

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		log.Printf("[coap] listening on %s (UDP)", a.cfg.Listen)
		if err := coap.ListenAndServe("udp", a.cfg.Listen, router); err != nil {
			// ListenAndServe returns error on shutdown — only log if unexpected.
			log.Printf("[coap] serve exited: %v", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the CoAP UDP server.
func (a *Adapter) Stop(ctx context.Context) error {
	if a.cancel != nil {
		a.cancel()
	}
	log.Println("[coap] adapter stopped")
	return nil
}

// ── Telemetry Handler ───────────────────────────────────

// handleTelemetry processes requests to /telemetry/<device_id>.
// Expects a JSON body: {"metric": "temperature", "value": 23.5, "unit": "celsius"}
func (a *Adapter) handleTelemetry(w mux.ResponseWriter, req *mux.Message) {
	deviceID := extractDeviceID(req, "telemetry")
	if deviceID == "" {
		setResponse(w, codes.BadRequest, "missing device_id in path")
		return
	}

	body, err := readBody(req)
	if err != nil {
		setResponse(w, codes.BadRequest, "cannot read body")
		return
	}

	var payload struct {
		Metric string  `json:"metric"`
		Value  float64 `json:"value"`
		Unit   string  `json:"unit"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		setResponse(w, codes.BadRequest, "invalid json")
		return
	}

	msg := canonical.NewTelemetryMessage(deviceID, "coap", payload.Metric, payload.Value, payload.Unit)
	data, err := canonical.Marshal(msg)
	if err != nil {
		setResponse(w, codes.InternalServerError, "marshal failed")
		return
	}

	if err := a.bus.Publish(canonical.Subject(msg), data); err != nil {
		setResponse(w, codes.InternalServerError, "publish failed")
		return
	}

	a.reg.Register(registry.Device{
		DeviceID: deviceID,
		Name:     deviceID,
		Protocol: "coap",
		Status:   "active",
	})

	setResponse(w, codes.Created, fmt.Sprintf(`{"message_id":"%s"}`, msg.GetMessageId()))
	log.Printf("[coap] telemetry from %s: %s=%.2f", deviceID, payload.Metric, payload.Value)
}

// ── Event Handler ───────────────────────────────────────

// handleEvent processes requests to /event/<device_id>.
// Expects a JSON body: {"event_type": "alarm", "severity": "critical", "detail": "overheated"}
func (a *Adapter) handleEvent(w mux.ResponseWriter, req *mux.Message) {
	deviceID := extractDeviceID(req, "event")
	if deviceID == "" {
		setResponse(w, codes.BadRequest, "missing device_id in path")
		return
	}

	body, err := readBody(req)
	if err != nil {
		setResponse(w, codes.BadRequest, "cannot read body")
		return
	}

	var payload struct {
		EventType string `json:"event_type"`
		Severity  string `json:"severity"`
		Detail    string `json:"detail"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		setResponse(w, codes.BadRequest, "invalid json")
		return
	}

	msg := canonical.NewEventMessage(deviceID, "coap", payload.EventType, payload.Severity, payload.Detail)
	data, err := canonical.Marshal(msg)
	if err != nil {
		setResponse(w, codes.InternalServerError, "marshal failed")
		return
	}

	if err := a.bus.Publish(canonical.Subject(msg), data); err != nil {
		setResponse(w, codes.InternalServerError, "publish failed")
		return
	}

	a.reg.Register(registry.Device{
		DeviceID: deviceID,
		Name:     deviceID,
		Protocol: "coap",
		Status:   "active",
	})

	setResponse(w, codes.Created, fmt.Sprintf(`{"message_id":"%s"}`, msg.GetMessageId()))
	log.Printf("[coap] event from %s: type=%s severity=%s", deviceID, payload.EventType, payload.Severity)
}

// ── Helpers ─────────────────────────────────────────────

// extractDeviceID pulls the device ID from the CoAP URI path.
// CoAP paths are split into URI-Path options by the library, so
// the full path is reconstructed and parsed.
// For a path "telemetry/sensor-99" with prefix "telemetry",
// it returns "sensor-99".
func extractDeviceID(req *mux.Message, prefix string) string {
	path, err := req.Options().Path()
	if err != nil {
		return ""
	}
	// path looks like "telemetry/sensor-99"
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 || parts[0] != prefix {
		return ""
	}
	return parts[1]
}

// readBody reads the full request body from a CoAP message.
func readBody(req *mux.Message) ([]byte, error) {
	if req.Body() == nil {
		return nil, fmt.Errorf("empty body")
	}
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(req.Body()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// setResponse writes a CoAP response with a text payload.
func setResponse(w mux.ResponseWriter, code codes.Code, body string) {
	err := w.SetResponse(code, message.TextPlain, bytes.NewReader([]byte(body)))
	if err != nil {
		log.Printf("[coap] cannot set response: %v", err)
	}
}
