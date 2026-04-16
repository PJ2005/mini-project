package gateway

import (
	"strings"
	"testing"

	adaptcoap "interlink/internal/adapter/coap"
	adapthttp "interlink/internal/adapter/http"
	adaptmqtt "interlink/internal/adapter/mqtt"
	"interlink/internal/pipeline"
	"interlink/internal/policy"
)

func validConfig() *Config {
	return &Config{
		NATS:     NATSConfig{URL: "nats://127.0.0.1:4222"},
		MQTT:     adaptmqtt.Config{Broker: "tcp://127.0.0.1:1883", ClientID: "gw-1", Topic: "devices/#", DeviceIDTopicIndex: 1},
		HTTP:     adapthttp.Config{Listen: ":8080", SSEChannelCap: 256},
		CoAP:     adaptcoap.Config{Listen: ":5683"},
		Registry: RegistryConfig{DBPath: "./interlink.db"},
		Policy:   policy.Config{DefaultAction: "allow", WorkerCount: 1},
		UI:       UIConfig{Listen: ":8081"},
		Pipelines: []pipeline.PipelineConfig{
			{
				Name: "p1",
				Sink: pipeline.SinkConfig{Type: "http_post", URL: "http://localhost:9000/ingest"},
			},
		},
	}
}

func TestValidateConfigAcceptsValidConfig(t *testing.T) {
	cfg := validConfig()
	if errs := validateConfig(cfg); len(errs) != 0 {
		t.Fatalf("validateConfig returned errors for valid config: %v", errs)
	}
}

func TestValidateConfigRejectsPolicyWorkerCount(t *testing.T) {
	cfg := validConfig()
	cfg.Policy.WorkerCount = 0

	errs := validateConfig(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation errors, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e, "policy.worker_count") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected policy.worker_count validation error, got: %v", errs)
	}
}

func TestValidateConfigRejectsInvalidHTTPPostURL(t *testing.T) {
	cfg := validConfig()
	cfg.Pipelines[0].Sink.URL = "dashboard.local/ingest"

	errs := validateConfig(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation errors, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e, "http_post sink url") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected http_post sink URL validation error, got: %v", errs)
	}
}

func TestValidateConfigRejectsUILocalConflict(t *testing.T) {
	cfg := validConfig()
	cfg.UI.Listen = cfg.HTTP.Listen

	errs := validateConfig(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation errors, got none")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e, "ui.listen") && strings.Contains(e, "http.listen") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected ui/http listen conflict error, got: %v", errs)
	}
}
