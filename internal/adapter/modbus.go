package adapter

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/goburrow/modbus"

	"interlink/internal/bus"
	"interlink/internal/canonical"
	"interlink/internal/metrics"
	"interlink/internal/registry"
)

type ModbusRegisterConfig struct {
	Name     string `yaml:"name"`
	Address  uint16 `yaml:"address"`
	Type     string `yaml:"type"`
	DeviceID string `yaml:"device_id"`
}

type ModbusConfig struct {
	Host           string                 `yaml:"host"`
	Port           int                    `yaml:"port"`
	PollIntervalMS int                    `yaml:"poll_interval_ms"`
	Registers      []ModbusRegisterConfig `yaml:"registers"`
}

type ModbusAdapter struct {
	cfg    ModbusConfig
	bus    bus.MessageBus
	reg    *registry.Registry
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewModbus(cfg ModbusConfig) *ModbusAdapter {
	return &ModbusAdapter{cfg: cfg}
}

func (a *ModbusAdapter) Name() string { return "modbus" }

func (a *ModbusAdapter) Start(ctx context.Context, b bus.MessageBus, reg *registry.Registry) error {
	a.bus = b
	a.reg = reg
	runCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel

	if a.cfg.PollIntervalMS <= 0 {
		a.cfg.PollIntervalMS = 1000
	}
	if a.cfg.Port <= 0 {
		a.cfg.Port = 502
	}

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.pollLoop(runCtx)
	}()

	slog.Info("adapter started",
		"component", "modbus",
		"host", a.cfg.Host,
		"port", a.cfg.Port,
		"registers", len(a.cfg.Registers),
		"poll_interval_ms", a.cfg.PollIntervalMS)
	return nil
}

func (a *ModbusAdapter) Stop(ctx context.Context) error {
	if a.cancel != nil {
		a.cancel()
	}
	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}

	slog.Info("adapter stopped", "component", "modbus")
	return nil
}

func (a *ModbusAdapter) pollLoop(ctx context.Context) {
	if a.cfg.Host == "" {
		slog.Warn("modbus host empty; polling disabled", "component", "modbus")
		return
	}

	address := a.cfg.Host + ":" + strconv.Itoa(a.cfg.Port)
	handler := modbus.NewTCPClientHandler(address)
	handler.Timeout = 5 * time.Second
	handler.SlaveId = 1

	if err := handler.Connect(); err != nil {
		slog.Error("modbus connect failed", "component", "modbus", "address", address, "error", err)
	}
	defer handler.Close()

	client := modbus.NewClient(handler)
	ticker := time.NewTicker(time.Duration(a.cfg.PollIntervalMS) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, r := range a.cfg.Registers {
				a.pollRegister(client, r)
			}
		}
	}
}

func (a *ModbusAdapter) pollRegister(client modbus.Client, r ModbusRegisterConfig) {
	if r.Name == "" || r.DeviceID == "" {
		slog.Warn("modbus register skipped: missing name or device_id",
			"component", "modbus",
			"name", r.Name,
			"device_id", r.DeviceID,
			"address", r.Address,
			"type", r.Type)
		return
	}

	value, err := readRegisterValue(client, r)
	if err != nil {
		slog.Error("modbus register read failed",
			"component", "modbus",
			"name", r.Name,
			"device_id", r.DeviceID,
			"address", r.Address,
			"type", r.Type,
			"error", err)
		return
	}

	msg := canonical.NewTelemetryMessage(r.DeviceID, "modbus", r.Name, value, "")
	data, err := canonical.MarshalPooled(msg)
	if err != nil {
		slog.Error("modbus marshal failed",
			"component", "modbus",
			"name", r.Name,
			"device_id", r.DeviceID,
			"error", err)
		return
	}

	publishStart := time.Now()
	if err := a.bus.Publish(canonical.Subject(msg), data); err != nil {
		slog.Error("modbus publish failed",
			"component", "modbus",
			"name", r.Name,
			"device_id", r.DeviceID,
			"error", err)
		return
	}
	metrics.ObserveMessageLatencyMS("modbus", "publish", time.Since(publishStart))

	if err := a.reg.Register(registry.Device{
		DeviceID: r.DeviceID,
		Name:     r.DeviceID,
		Protocol: "modbus",
		Status:   "active",
	}); err != nil {
		slog.Error("modbus registry upsert failed",
			"component", "modbus",
			"device_id", r.DeviceID,
			"error", err)
	}
}

func readRegisterValue(client modbus.Client, r ModbusRegisterConfig) (float64, error) {
	switch r.Type {
	case "coil":
		resp, err := client.ReadCoils(r.Address, 1)
		if err != nil {
			return 0, err
		}
		if len(resp) == 0 {
			return 0, fmt.Errorf("empty coil response")
		}
		if resp[0]&0x01 == 1 {
			return 1, nil
		}
		return 0, nil
	case "holding":
		resp, err := client.ReadHoldingRegisters(r.Address, 1)
		if err != nil {
			return 0, err
		}
		if len(resp) < 2 {
			return 0, fmt.Errorf("holding register response too short")
		}
		return float64(binary.BigEndian.Uint16(resp[:2])), nil
	case "input":
		resp, err := client.ReadInputRegisters(r.Address, 1)
		if err != nil {
			return 0, err
		}
		if len(resp) < 2 {
			return 0, fmt.Errorf("input register response too short")
		}
		return float64(binary.BigEndian.Uint16(resp[:2])), nil
	default:
		return 0, fmt.Errorf("unsupported register type %q", r.Type)
	}
}
