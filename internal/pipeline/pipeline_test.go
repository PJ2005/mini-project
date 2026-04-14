package pipeline

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"interlink/internal/bus"
	"interlink/internal/canonical"
	"interlink/internal/registry"
)

func TestPipelineNATSScaleFlow(t *testing.T) {
	b, err := bus.Connect("nats://127.0.0.1:4222")
	if err != nil {
		t.Skip("nats not available")
	}
	defer b.Close()

	reg, err := registry.New(filepath.Join(t.TempDir(), "pipeline.db"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	defer reg.Close()

	ns := fmt.Sprintf("pipeline.%d", time.Now().UnixNano())
	inSubject := ns + ".in"
	outSubject := ns + ".out"

	p, err := New(PipelineConfig{
		Name:   "c-to-f",
		Source: SourceConfig{Type: "nats", Subject: inSubject},
		Transforms: []TransformConfig{
			{Type: "extract", Field: "temp", As: "value"},
			{Type: "scale", Factor: 1.8, Offset: 32, As: "value"},
		},
		Sink: SinkConfig{Type: "nats_publish", Subject: outSubject, Timeout: 3 * time.Second},
	}, b, reg)
	if err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("start pipeline: %v", err)
	}
	defer p.Stop(context.Background())

	outCh := make(chan *canonical.Message, 2)
	sub, err := b.Subscribe(outSubject, func(_ string, data []byte) {
		msg, uErr := canonical.UnmarshalMessage(data)
		if uErr != nil {
			return
		}
		select {
		case outCh <- msg:
		default:
		}
	})
	if err != nil {
		t.Fatalf("subscribe out subject: %v", err)
	}
	defer sub.Unsubscribe()

	check := func(in, want float64) {
		msg := canonical.NewTelemetryMessage("sensor-1", "nats", "temp", in, "C")
		data, mErr := canonical.MarshalPooled(msg)
		if mErr != nil {
			t.Fatalf("marshal input: %v", mErr)
		}
		if pErr := b.Publish(inSubject, data); pErr != nil {
			t.Fatalf("publish input: %v", pErr)
		}

		select {
		case got := <-outCh:
			val := got.GetTelemetry().GetValue()
			if math.Abs(val-want) > 1e-9 {
				t.Fatalf("output value = %v, want %v", val, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for transformed value %v", want)
		}
	}

	check(0.0, 32.0)
	check(100.0, 212.0)
}
