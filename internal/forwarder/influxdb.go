package forwarder

import (
	"context"
	"log/slog"
	"sync"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"

	"interlink/internal/bus"
	"interlink/internal/canonical"
)

type InfluxDBConfig struct {
	URL    string `yaml:"url"`
	Token  string `yaml:"token"`
	Org    string `yaml:"org"`
	Bucket string `yaml:"bucket"`
}

type InfluxDBForwarder struct {
	cfg    InfluxDBConfig
	bus    bus.MessageBus
	sub    bus.Subscription
	client influxdb2.Client
	write  api.WriteAPI
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewInfluxDB(cfg InfluxDBConfig) *InfluxDBForwarder {
	return &InfluxDBForwarder{cfg: cfg}
}

func (f *InfluxDBForwarder) Name() string { return "influxdb" }

func (f *InfluxDBForwarder) Start(ctx context.Context, b bus.MessageBus) error {
	f.bus = b
	runCtx, cancel := context.WithCancel(ctx)
	f.cancel = cancel

	opts := influxdb2.DefaultOptions().
		SetBatchSize(100).
		SetFlushInterval(1000)
	f.client = influxdb2.NewClientWithOptions(f.cfg.URL, f.cfg.Token, opts)
	f.write = f.client.WriteAPI(f.cfg.Org, f.cfg.Bucket)

	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			select {
			case <-runCtx.Done():
				return
			case err, ok := <-f.write.Errors():
				if !ok {
					return
				}
				slog.Error("influxdb write error",
					"component", "forwarder/influxdb",
					"error", err)
			}
		}
	}()

	sub, err := b.Subscribe("iot.telemetry.>", func(subject string, data []byte) {
		_ = subject
		f.forwardTelemetry(data)
	})
	if err != nil {
		cancel()
		return err
	}
	f.sub = sub

	slog.Info("forwarder started",
		"component", "forwarder/influxdb",
		"url", f.cfg.URL,
		"org", f.cfg.Org,
		"bucket", f.cfg.Bucket,
		"batch_size", 100,
		"flush_interval_ms", 1000)
	return nil
}

func (f *InfluxDBForwarder) Stop(ctx context.Context) error {
	if f.cancel != nil {
		f.cancel()
	}
	if f.sub != nil {
		if err := f.sub.Unsubscribe(); err != nil {
			slog.Warn("influxdb forwarder unsubscribe failed",
				"component", "forwarder/influxdb",
				"error", err)
		}
	}
	if f.write != nil {
		f.write.Flush()
	}
	if f.client != nil {
		f.client.Close()
	}

	done := make(chan struct{})
	go func() {
		f.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}

	slog.Info("forwarder stopped", "component", "forwarder/influxdb")
	return nil
}

func (f *InfluxDBForwarder) forwardTelemetry(data []byte) {
	msg, err := canonical.UnmarshalMessage(data)
	if err != nil {
		slog.Warn("influxdb forwarder dropped invalid canonical message",
			"component", "forwarder/influxdb",
			"error", err)
		return
	}
	tel := msg.GetTelemetry()
	if tel == nil {
		return
	}

	point := influxdb2.NewPointWithMeasurement(msg.GetDeviceId()).
		AddField(tel.GetMetric(), tel.GetValue()).
		SetTime(time.UnixMilli(msg.GetTimestampMs()))
	f.write.WritePoint(point)
}
