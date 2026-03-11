package coap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
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

type Config struct {
	Listen string `yaml:"listen"`
}

var validDeviceID = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type Adapter struct {
	cfg    Config
	bus    bus.MessageBus
	reg    *registry.Registry
	wg     sync.WaitGroup
	cancel context.CancelFunc
}

func New(cfg Config) *Adapter {
	return &Adapter{cfg: cfg}
}

func (a *Adapter) Name() string { return "coap" }

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
		slog.Info("listening", "component", "coap", "listen", a.cfg.Listen, "transport", "UDP")
		if err := coap.ListenAndServe("udp", a.cfg.Listen, router); err != nil {
			slog.Error("serve exited", "component", "coap", "error", err)
		}
	}()

	return nil
}

func (a *Adapter) Stop(ctx context.Context) error {
	if a.cancel != nil {
		a.cancel()
	}
	slog.Info("adapter stopped", "component", "coap")
	return nil
}

func (a *Adapter) handleTelemetry(w mux.ResponseWriter, req *mux.Message) {
	deviceID, ok := a.validateDeviceID(w, req, "telemetry")
	if !ok {
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
	slog.Info("telemetry received",
		"component", "coap",
		"device_id", deviceID,
		"metric", payload.Metric,
		"value", payload.Value)
}

func (a *Adapter) handleEvent(w mux.ResponseWriter, req *mux.Message) {
	deviceID, ok := a.validateDeviceID(w, req, "event")
	if !ok {
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
	slog.Info("event received",
		"component", "coap",
		"device_id", deviceID,
		"event_type", payload.EventType,
		"severity", payload.Severity)
}

func (a *Adapter) validateDeviceID(w mux.ResponseWriter, req *mux.Message, prefix string) (string, bool) {
	deviceID := extractDeviceID(req, prefix)
	if deviceID == "" {
		setResponse(w, codes.BadRequest, "device_id is empty or missing from path")
		return "", false
	}
	if !validDeviceID.MatchString(deviceID) {
		setResponse(w, codes.BadRequest,
			fmt.Sprintf("device_id %q contains invalid characters; only alphanumerics, dashes, and underscores are allowed", deviceID))
		return "", false
	}
	return deviceID, true
}

func extractDeviceID(req *mux.Message, prefix string) string {
	path, err := req.Options().Path()
	if err != nil {
		return ""
	}
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 || parts[0] != prefix {
		return ""
	}
	return parts[1]
}

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

func setResponse(w mux.ResponseWriter, code codes.Code, body string) {
	err := w.SetResponse(code, message.TextPlain, bytes.NewReader([]byte(body)))
	if err != nil {
		slog.Error("cannot set response", "component", "coap", "error", err)
	}
}
