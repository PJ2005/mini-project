package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"interlink/internal/pipeline"
	"interlink/internal/registry"
)

func TestUIServerAPIs(t *testing.T) {
	reg, err := registry.New(filepath.Join(t.TempDir(), "ui.db"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer reg.Close()

	if err := reg.Register(registry.Device{DeviceID: "dev-1", Name: "dev-1", Protocol: "mqtt", Status: "active"}); err != nil {
		t.Fatalf("register device: %v", err)
	}

	pl, err := pipeline.New(pipeline.PipelineConfig{
		Name:   "ui-test-pipeline",
		Source: pipeline.SourceConfig{Type: "nats", Subject: "ui.in"},
		Sink:   pipeline.SinkConfig{Type: "nats_publish", Subject: "ui.out"},
	}, nil, reg)
	if err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	u := New([]*pipeline.Pipeline{pl}, reg)
	s := httptest.NewServer(u.Handler())
	defer s.Close()

	respP, err := http.Get(s.URL + "/api/ui/pipelines")
	if err != nil {
		t.Fatalf("get pipelines: %v", err)
	}
	defer respP.Body.Close()
	var pipes []map[string]any
	if err := json.NewDecoder(respP.Body).Decode(&pipes); err != nil {
		t.Fatalf("decode pipelines: %v", err)
	}
	if len(pipes) == 0 {
		t.Fatal("pipelines response is empty")
	}
	for _, k := range []string{"name", "source_type", "sink_type", "messages_processed", "last_message_at", "error_count"} {
		if _, ok := pipes[0][k]; !ok {
			t.Fatalf("pipelines missing field %q", k)
		}
	}

	respD, err := http.Get(s.URL + "/api/ui/devices")
	if err != nil {
		t.Fatalf("get devices: %v", err)
	}
	defer respD.Body.Close()
	var devices []map[string]any
	if err := json.NewDecoder(respD.Body).Decode(&devices); err != nil {
		t.Fatalf("decode devices: %v", err)
	}
	if len(devices) == 0 {
		t.Fatal("devices response is empty")
	}
	for _, k := range []string{"device_id", "protocol", "status"} {
		if _, ok := devices[0][k]; !ok {
			t.Fatalf("devices missing field %q", k)
		}
	}
}
