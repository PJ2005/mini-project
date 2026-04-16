package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"interlink/internal/adapter"
	adaptcoap "interlink/internal/adapter/coap"
	adapthttp "interlink/internal/adapter/http"
	adaptmqtt "interlink/internal/adapter/mqtt"
	"interlink/internal/bus"
	"interlink/internal/metrics"
	"interlink/internal/pipeline"
	"interlink/internal/policy"
	"interlink/internal/registry"
	"interlink/internal/ui"
)

type Config struct {
	NATS             NATSConfig                `yaml:"nats"`
	MQTT             adaptmqtt.Config          `yaml:"mqtt"`
	HTTP             adapthttp.Config          `yaml:"http"`
	CoAP             adaptcoap.Config          `yaml:"coap"`
	Modbus           adapter.ModbusConfig      `yaml:"modbus"`
	WebSocket        adapter.WebSocketConfig   `yaml:"websocket"`
	AMQP             adapter.AMQPConfig        `yaml:"amqp"`
	Registry         RegistryConfig            `yaml:"registry"`
	Policy           policy.Config             `yaml:"policy"`
	Pipelines        []pipeline.PipelineConfig `yaml:"pipelines"`
	UI               UIConfig                  `yaml:"ui"`
	HeartbeatTimeout Duration                  `yaml:"heartbeat_timeout"`
}

type UIConfig struct {
	Listen string `yaml:"listen"`
}

type NATSConfig struct {
	URL       string `yaml:"url"`
	JetStream bool   `yaml:"jetstream"`
}

type RegistryConfig struct {
	DBPath string `yaml:"db_path"`
}

// Duration wraps time.Duration for YAML unmarshalling from strings like "5m".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(dur)
	return nil
}

func Run(configPath string) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if cfg.UI.Listen == "" {
		cfg.UI.Listen = ":8081"
	}

	// Fix 16: validate config before establishing any connections.
	if errs := validateConfig(cfg); len(errs) > 0 {
		if cfg.UI.Listen == cfg.HTTP.Listen {
			slog.Error("fatal config error: ui.listen must differ from http.listen",
				"component", "gateway",
				"ui_listen", cfg.UI.Listen,
				"http_listen", cfg.HTTP.Listen)
		}
		fmt.Fprintln(os.Stderr, "Configuration validation failed:")
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  • %s\n", e)
		}
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Initialize centralized metrics.
	metrics.Init()

	// NATS — Fix 11: JetStream opt-in.
	var msgBus bus.MessageBus
	if cfg.NATS.JetStream {
		jsBus, err := bus.ConnectJetStream(cfg.NATS.URL)
		if err != nil {
			return err
		}
		defer jsBus.Close()
		msgBus = jsBus
		slog.Info("connected to NATS JetStream", "component", "gateway", "url", cfg.NATS.URL)
	} else {
		natsBus, err := bus.Connect(cfg.NATS.URL)
		if err != nil {
			return err
		}
		defer natsBus.Close()

		// Fix 8: register reconnect/disconnect handlers.
		natsBus.SetDisconnectHandler(func(err error) {
			slog.Warn("NATS disconnected", "component", "gateway", "error", err)
		})
		natsBus.SetReconnectHandler(func() {
			slog.Info("NATS reconnected — subscriptions auto-restored", "component", "gateway")
		})

		msgBus = natsBus
		slog.Info("connected to NATS", "component", "gateway", "url", cfg.NATS.URL)
	}

	// Registry
	reg, err := registry.New(cfg.Registry.DBPath)
	if err != nil {
		return err
	}
	defer reg.Close()
	slog.Info("registry opened", "component", "gateway", "db_path", cfg.Registry.DBPath)

	// Policy — Fix 4: provide bus reference for command routing.
	engine := policy.New(cfg.Policy)
	engine.SetBus(msgBus)
	engine.StartWorkerPool(ctx, cfg.Policy.WorkerCount)
	slog.Info("policy engine loaded",
		"component", "gateway",
		"rules", len(cfg.Policy.Rules),
		"default_action", cfg.Policy.DefaultAction,
		"worker_count", cfg.Policy.WorkerCount)

	// Fix 5: start policy hot-reload file watcher.
	if err := engine.WatchConfig(ctx, configPath); err != nil {
		slog.Error("policy watcher start error", "component", "gateway", "error", err)
	}

	// Wire policy as a NATS subscription interceptor.
	_, err = msgBus.Subscribe("iot.>", func(subject string, data []byte) {
		if !engine.Submit(subject, data) {
			metrics.PolicyDroppedTotal.Inc()
		}
	})
	if err != nil {
		return fmt.Errorf("policy interceptor subscribe: %w", err)
	}

	// Adapters
	httpAdapter := adapthttp.New(cfg.HTTP)
	adapters := []adapter.Adapter{
		adaptmqtt.New(cfg.MQTT),
		httpAdapter,
		adaptcoap.New(cfg.CoAP),
	}
	if cfg.Modbus.Host != "" && len(cfg.Modbus.Registers) > 0 {
		adapters = append(adapters, adapter.NewModbus(cfg.Modbus))
	}
	if cfg.WebSocket.Listen != "" {
		adapters = append(adapters, adapter.NewWebSocket(cfg.WebSocket))
	}
	if cfg.AMQP.URL != "" && cfg.AMQP.Queue != "" {
		adapters = append(adapters, adapter.NewAMQP(cfg.AMQP))
	}

	adapterNames := make([]string, len(adapters))
	for i, a := range adapters {
		adapterNames[i] = a.Name()
	}
	httpAdapter.SetAdapterNames(adapterNames)
	httpAdapter.SetPolicyEngine(engine)

	for _, a := range adapters {
		if err := a.Start(ctx, msgBus, reg); err != nil {
			return fmt.Errorf("start adapter %s: %w", a.Name(), err)
		}
		slog.Info("adapter started", "component", "gateway", "adapter", a.Name())
	}

	var pipelines []*pipeline.Pipeline
	for _, pc := range cfg.Pipelines {
		pl, err := pipeline.New(pc, msgBus, reg)
		if err != nil {
			slog.Error("pipeline init error",
				"component", "gateway",
				"pipeline", pc.Name,
				"error", err)
			continue
		}
		if err := pl.Start(ctx); err != nil {
			slog.Error("pipeline start error",
				"component", "gateway",
				"pipeline", pc.Name,
				"error", err)
			continue
		}
		pipelines = append(pipelines, pl)
	}

	uiServer := ui.New(pipelines, reg)
	uiHTTP := &http.Server{Addr: cfg.UI.Listen, Handler: uiServer.Handler()}
	go func() {
		slog.Info("InterLink UI available at http://localhost:8081",
			"component", "gateway",
			"listen", cfg.UI.Listen)
		if err := uiHTTP.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("ui server error", "component", "gateway", "error", err)
		}
	}()

	// Start metrics background collectors.
	metrics.StartCollector(ctx, reg, 10*time.Second)
	httpAdapter.StartGaugeRefresh(ctx)

	// Fix 1: heartbeat timeout background goroutine.
	heartbeatTimeout := time.Duration(cfg.HeartbeatTimeout)
	if heartbeatTimeout == 0 {
		heartbeatTimeout = 5 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n, err := reg.MarkInactiveDevices(heartbeatTimeout)
				if err != nil {
					slog.Error("heartbeat check error", "component", "gateway", "error", err)
				} else if n > 0 {
					slog.Info("heartbeat: marked devices inactive",
						"component", "gateway",
						"count", n,
						"timeout", heartbeatTimeout.String())
				}
			}
		}
	}()

	slog.Info("InterLink is running", "component", "gateway")
	<-ctx.Done()
	slog.Info("shutting down...", "component", "gateway")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5_000_000_000) // 5s
	defer shutdownCancel()

	for _, p := range pipelines {
		p.Stop(shutdownCtx)
	}

	uiShutdownCtx, uiShutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := uiHTTP.Shutdown(uiShutdownCtx); err != nil {
		slog.Error("ui shutdown error", "component", "gateway", "error", err)
	}
	uiShutdownCancel()

	for _, a := range adapters {
		if err := a.Stop(shutdownCtx); err != nil {
			slog.Error("error stopping adapter", "component", "gateway", "adapter", a.Name(), "error", err)
		}
	}

	slog.Info("shutdown complete", "component", "gateway")
	return nil
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

// ── Config Validation (Fix 16) ──────────────────────────

func validateConfig(cfg *Config) []string {
	var errs []string

	if cfg.NATS.URL == "" {
		errs = append(errs, "nats.url is required and must be non-empty")
	}
	if cfg.Registry.DBPath == "" {
		errs = append(errs, "registry.db_path is required and must be non-empty")
	}
	if cfg.HTTP.Listen == "" {
		errs = append(errs, "http.listen is required and must be non-empty")
	}
	if cfg.MQTT.Broker == "" {
		errs = append(errs, "mqtt.broker is required and must be non-empty")
	}
	if cfg.MQTT.ClientID == "" {
		errs = append(errs, "mqtt.client_id is required and must be non-empty")
	}
	if cfg.MQTT.Topic == "" {
		errs = append(errs, "mqtt.topic is required and must be non-empty")
	}
	if cfg.CoAP.Listen == "" {
		errs = append(errs, "coap.listen is required and must be non-empty")
	}
	if cfg.UI.Listen == "" {
		errs = append(errs, "ui.listen is required and must be non-empty")
	}
	if cfg.UI.Listen == cfg.HTTP.Listen {
		errs = append(errs, fmt.Sprintf("ui.listen %q must differ from http.listen %q", cfg.UI.Listen, cfg.HTTP.Listen))
	}

	validActions := map[string]bool{"allow": true, "deny": true}
	if cfg.Policy.DefaultAction != "" && !validActions[cfg.Policy.DefaultAction] {
		errs = append(errs, fmt.Sprintf("policy.default_action %q must be 'allow' or 'deny'", cfg.Policy.DefaultAction))
	}
	for i, rule := range cfg.Policy.Rules {
		action := strings.ToLower(rule.Action)
		if !validActions[action] {
			errs = append(errs, fmt.Sprintf("policy.rules[%d].action %q must be 'allow' or 'deny'", i, rule.Action))
		}
	}
	if cfg.Policy.WorkerCount < 1 {
		errs = append(errs, fmt.Sprintf("policy.worker_count %d must be >= 1", cfg.Policy.WorkerCount))
	}

	for i, pl := range cfg.Pipelines {
		if pl.Sink.Type != "http_post" {
			continue
		}
		if pl.Sink.URL == "" {
			errs = append(errs, fmt.Sprintf("pipelines[%d] (%s): http_post sink requires non-empty url", i, pl.Name))
			continue
		}
		u, err := url.Parse(pl.Sink.URL)
		if err != nil || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			errs = append(errs, fmt.Sprintf("pipelines[%d] (%s): http_post sink url %q must be a valid http/https URL", i, pl.Name, pl.Sink.URL))
		}
	}

	return errs
}
