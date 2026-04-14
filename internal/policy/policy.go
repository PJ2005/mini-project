package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"

	"interlink/internal/bus"
	"interlink/internal/canonical"
	"interlink/internal/metrics"
)

type Rule struct {
	DevicePattern string `yaml:"device_pattern"`
	SourceProto   string `yaml:"source_proto"`
	MessageType   string `yaml:"message_type"`
	Action        string `yaml:"action"`
	CommandTarget string `yaml:"command_target,omitempty"`
	CommandAction string `yaml:"command_action,omitempty"`
}

type Config struct {
	DefaultAction string `yaml:"default_action"`
	WorkerCount   int    `yaml:"worker_count"`
	Rules         []Rule `yaml:"rules"`
}

// Counters tracks allow/deny decisions for Prometheus metrics (Fix 15).
type Counters struct {
	mu      sync.Mutex
	Allowed int64
	Denied  int64
}

func (c *Counters) incAllowed() { c.mu.Lock(); c.Allowed++; c.mu.Unlock() }
func (c *Counters) incDenied()  { c.mu.Lock(); c.Denied++; c.mu.Unlock() }

func (c *Counters) GetCounts() (allowed, denied int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Allowed, c.Denied
}

type Engine struct {
	mu            sync.RWMutex
	rules         []Rule
	defaultAction string
	msgBus        bus.MessageBus
	submitCh      chan policyWorkItem
	Stats         Counters
}

type policyWorkItem struct {
	subject string
	data    []byte
}

func New(cfg Config) *Engine {
	def := cfg.DefaultAction
	if def == "" {
		def = "allow"
	}
	return &Engine{
		rules:         cfg.Rules,
		defaultAction: def,
	}
}

func (e *Engine) SetBus(b bus.MessageBus) {
	e.msgBus = b
}

func (e *Engine) StartWorkerPool(ctx context.Context, workerCount int) {
	if workerCount <= 0 {
		workerCount = runtime.NumCPU()
	}
	if e.submitCh == nil {
		e.submitCh = make(chan policyWorkItem, 2048)
	}
	for i := 0; i < workerCount; i++ {
		go e.policyWorker(ctx)
	}
	slog.Info("policy worker pool started",
		"component", "policy",
		"workers", workerCount,
		"queue_capacity", cap(e.submitCh))
}

func (e *Engine) Submit(subject string, data []byte) bool {
	if e.submitCh == nil {
		slog.Warn("policy: worker pool not started",
			"component", "policy",
			"subject", subject)
		return false
	}
	item := policyWorkItem{subject: subject, data: append([]byte(nil), data...)}
	select {
	case e.submitCh <- item:
		return true
	default:
		slog.Warn("policy: ingestion buffer full, dropping message",
			"component", "policy",
			"subject", subject)
		return false
	}
}

func (e *Engine) policyWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-e.submitCh:
			e.processWorkItem(item)
		}
	}
}

func (e *Engine) processWorkItem(item policyWorkItem) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic recovered in policy worker",
				"component", "policy",
				"subject", item.subject,
				"error", fmt.Sprintf("%v", r))
		}
	}()

	msg, err := canonical.UnmarshalMessage(item.data)
	if err != nil {
		slog.Error("policy unmarshal error",
			"component", "policy",
			"subject", item.subject,
			"error", err)
		return
	}
	e.Evaluate(msg)
}

func (e *Engine) Evaluate(msg *canonical.Message) bool {
	start := time.Now()
	defer func() {
		metrics.PolicyEvalDuration.Observe(time.Since(start).Seconds())
	}()

	e.mu.RLock()
	rules := e.rules
	defaultAction := e.defaultAction
	e.mu.RUnlock()

	msgType := canonical.SubjectType(msg)
	deviceID := msg.GetDeviceId()
	sourceProto := msg.GetSourceProto()

	for _, r := range rules {
		if !matchField(r.MessageType, msgType) {
			continue
		}
		if !matchField(r.DevicePattern, deviceID) {
			continue
		}
		if !matchField(r.SourceProto, sourceProto) {
			continue
		}
		allowed := r.Action == "allow"
		if allowed {
			e.Stats.incAllowed()
			metrics.PolicyDecisions.WithLabelValues("allow").Inc()
			e.triggerCommand(r, msg)
		} else {
			e.Stats.incDenied()
			metrics.PolicyDecisions.WithLabelValues("deny").Inc()
			slog.Warn("policy denied message",
				"component", "policy",
				"device_id", deviceID,
				"message_type", msgType,
				"source_proto", sourceProto)
		}
		return allowed
	}

	if defaultAction == "allow" {
		e.Stats.incAllowed()
		metrics.PolicyDecisions.WithLabelValues("allow").Inc()
	} else {
		e.Stats.incDenied()
		metrics.PolicyDecisions.WithLabelValues("deny").Inc()
	}
	return defaultAction == "allow"
}

func (e *Engine) triggerCommand(r Rule, srcMsg *canonical.Message) {
	if r.CommandTarget == "" || r.CommandAction == "" || e.msgBus == nil {
		return
	}
	params, _ := json.Marshal(map[string]string{
		"triggered_by": srcMsg.GetDeviceId(),
	})
	cmd := canonical.NewCommandMessage(r.CommandTarget, "policy", r.CommandAction, params)
	data, err := canonical.MarshalPooled(cmd)
	if err != nil {
		slog.Error("command marshal error", "component", "policy", "error", err)
		return
	}
	if err := e.msgBus.Publish(canonical.Subject(cmd), data); err != nil {
		slog.Error("command publish failed",
			"component", "policy",
			"target", r.CommandTarget,
			"error", err)
	} else {
		slog.Info("routed command",
			"component", "policy",
			"action", r.CommandAction,
			"target", r.CommandTarget)
	}
}

// ── Hot-Reload (Fix 5) ─────────────────────────────────

type fileConfig struct {
	Policy Config `yaml:"policy"`
}

func (e *Engine) WatchConfig(ctx context.Context, configPath string) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify watcher: %w", err)
	}

	dir := filepath.Dir(configPath)
	base := filepath.Base(configPath)
	if err := watcher.Add(dir); err != nil {
		watcher.Close()
		return fmt.Errorf("watch dir %s: %w", dir, err)
	}

	go func() {
		defer watcher.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-watcher.Events:
				if !ok {
					return
				}
				if filepath.Base(ev.Name) != base {
					continue
				}
				if ev.Op&(fsnotify.Write|fsnotify.Create) == 0 {
					continue
				}
				e.reload(configPath)
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				slog.Error("watcher error", "component", "policy", "error", err)
			}
		}
	}()

	slog.Info("watching config for hot-reload", "component", "policy", "path", configPath)
	return nil
}

func (e *Engine) reload(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Error("reload read error", "component", "policy", "error", err)
		return
	}
	var fc fileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		slog.Error("reload parse error", "component", "policy", "error", err)
		return
	}
	def := fc.Policy.DefaultAction
	if def == "" {
		def = "allow"
	}
	e.mu.Lock()
	e.rules = fc.Policy.Rules
	e.defaultAction = def
	e.mu.Unlock()
	slog.Info("hot-reloaded policy",
		"component", "policy",
		"rules", len(fc.Policy.Rules),
		"default_action", def)
}

func matchField(pattern, value string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	matched, err := filepath.Match(pattern, value)
	if err != nil {
		return false
	}
	return matched
}
