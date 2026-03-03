package gateway

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"gopkg.in/yaml.v3"

	"edgemesh/internal/adapter"
	adaptcoap "edgemesh/internal/adapter/coap"
	adapthttp "edgemesh/internal/adapter/http"
	adaptmqtt "edgemesh/internal/adapter/mqtt"
	"edgemesh/internal/bus"
	"edgemesh/internal/canonical"
	"edgemesh/internal/policy"
	"edgemesh/internal/registry"
)

type Config struct {
	NATS     NATSConfig       `yaml:"nats"`
	MQTT     adaptmqtt.Config `yaml:"mqtt"`
	HTTP     adapthttp.Config `yaml:"http"`
	CoAP     adaptcoap.Config `yaml:"coap"`
	Registry RegistryConfig   `yaml:"registry"`
	Policy   policy.Config    `yaml:"policy"`
}

type NATSConfig struct {
	URL string `yaml:"url"`
}

type RegistryConfig struct {
	DBPath string `yaml:"db_path"`
}

func Run(configPath string) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// NATS
	msgBus, err := bus.Connect(cfg.NATS.URL)
	if err != nil {
		return err
	}
	defer msgBus.Close()
	log.Printf("[gateway] connected to NATS at %s", cfg.NATS.URL)

	// Registry
	reg, err := registry.New(cfg.Registry.DBPath)
	if err != nil {
		return err
	}
	defer reg.Close()
	log.Printf("[gateway] registry opened at %s", cfg.Registry.DBPath)

	// Policy
	engine := policy.New(cfg.Policy)
	log.Printf("[gateway] policy engine loaded (%d rules, default=%s)", len(cfg.Policy.Rules), cfg.Policy.DefaultAction)

	// Wire policy as a NATS subscription interceptor on all iot messages.
	// Messages that fail policy are logged and dropped.
	_, err = msgBus.Subscribe("iot.>", func(subject string, data []byte) {
		msg, mErr := canonical.UnmarshalMessage(data)
		if mErr != nil {
			log.Printf("[gateway] unmarshal error on %s: %v", subject, mErr)
			return
		}
		if !engine.Evaluate(msg) {
			return
		}
		// Policy passed — message continues flowing to other subscribers.
		// No re-publish needed since NATS fan-out delivers to all subscribers.
	})
	if err != nil {
		return fmt.Errorf("policy interceptor subscribe: %w", err)
	}

	// Adapters
	adapters := []adapter.Adapter{
		adaptmqtt.New(cfg.MQTT),
		adapthttp.New(cfg.HTTP),
		adaptcoap.New(cfg.CoAP),
	}

	for _, a := range adapters {
		if err := a.Start(ctx, msgBus, reg); err != nil {
			return fmt.Errorf("start adapter %s: %w", a.Name(), err)
		}
		log.Printf("[gateway] adapter %s started", a.Name())
	}

	log.Println("[gateway] EdgeMesh is running. Press Ctrl+C to stop.")
	<-ctx.Done()
	log.Println("[gateway] shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5_000_000_000) // 5s
	defer shutdownCancel()

	for _, a := range adapters {
		if err := a.Stop(shutdownCtx); err != nil {
			log.Printf("[gateway] error stopping %s: %v", a.Name(), err)
		}
	}

	log.Println("[gateway] shutdown complete")
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
