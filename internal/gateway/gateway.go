package gateway

import (
	"context"
	"fmt"
	"log/slog"
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
	"interlink/internal/canonical"
	"interlink/internal/metrics"
	"interlink/internal/policy"
	"interlink/internal/registry"
)

type Config struct {
	NATS             NATSConfig       `yaml:"nats"`
	MQTT             adaptmqtt.Config `yaml:"mqtt"`
	HTTP             adapthttp.Config `yaml:"http"`
	CoAP             adaptcoap.Config `yaml:"coap"`
	Registry         RegistryConfig   `yaml:"registry"`
	Policy           policy.Config    `yaml:"policy"`
	HeartbeatTimeout Duration         `yaml:"heartbeat_timeout"`
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

	// Fix 16: validate config before establishing any connections.
	if errs := validateConfig(cfg); len(errs) > 0 {
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
	slog.Info("policy engine loaded",
		"component", "gateway",
		"rules", len(cfg.Policy.Rules),
		"default_action", cfg.Policy.DefaultAction)

	// Fix 5: start policy hot-reload file watcher.
	if err := engine.WatchConfig(ctx, configPath); err != nil {
		slog.Error("policy watcher start error", "component", "gateway", "error", err)
	}

	// Wire policy as a NATS subscription interceptor.
	_, err = msgBus.Subscribe("iot.>", func(subject string, data []byte) {
		msg, mErr := canonical.UnmarshalMessage(data)
		if mErr != nil {
			slog.Error("unmarshal error", "component", "gateway", "subject", subject, "error", mErr)
			return
		}
		if !engine.Evaluate(msg) {
			return
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

	return errs
}
