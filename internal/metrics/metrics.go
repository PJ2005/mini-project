package metrics

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// ── Metric Definitions ─────────────────────────────────

var (
	// Throughput
	MessagesPublished = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "interlink_messages_published_total",
		Help: "Total messages published to NATS per adapter",
	}, []string{"adapter"})

	MessagesReceived = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "interlink_messages_received_total",
		Help: "Total messages received from devices per adapter",
	}, []string{"adapter"})

	// Latency
	MessageProcessingDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "interlink_message_processing_seconds",
		Help:    "Time to process a message through the adapter pipeline",
		Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1},
	}, []string{"adapter", "stage"})

	NATSPublishDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "interlink_nats_publish_seconds",
		Help:    "NATS publish latency",
		Buckets: []float64{0.00005, 0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05},
	})

	HTTPRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "interlink_http_request_seconds",
		Help:    "HTTP request processing latency",
		Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
	}, []string{"method", "path"})

	PolicyEvalDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "interlink_policy_evaluation_seconds",
		Help:    "Policy engine evaluation latency",
		Buckets: []float64{0.000001, 0.000005, 0.00001, 0.00005, 0.0001, 0.0005, 0.001},
	})

	// Policy
	PolicyDecisions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "interlink_policy_decisions_total",
		Help: "Policy allow/deny decision counts",
	}, []string{"action"})

	PolicyDroppedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "interlink_policy_dropped_total",
		Help: "Number of policy messages dropped before worker processing",
	})

	// SSE
	SSEClientsActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "interlink_sse_clients_active",
		Help: "Number of active SSE streaming clients",
	})

	SSEDroppedMessagesTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "interlink_sse_dropped_messages_total",
		Help: "Number of SSE messages dropped for slow consumers",
	})

	// Registry
	RegistryDevices = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "interlink_registry_devices",
		Help: "Number of registered devices by protocol and status",
	}, []string{"protocol", "status"})

	// Storage
	SQLiteDBSizeBytes = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "interlink_sqlite_db_size_bytes",
		Help: "SQLite database file size in bytes",
	})

	DeadLettersTotal = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "interlink_dead_letters_total",
		Help: "Number of messages in the dead letter queue",
	})

	// Memory
	GoHeapAllocBytes = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "interlink_go_heap_alloc_bytes",
		Help: "Go heap bytes allocated and still in use",
	})

	GoHeapInuseBytes = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "interlink_go_heap_inuse_bytes",
		Help: "Go heap bytes in active spans",
	})

	GoStackInuseBytes = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "interlink_go_stack_inuse_bytes",
		Help: "Go stack bytes in use",
	})

	GoSysBytes = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "interlink_go_sys_bytes",
		Help: "Total bytes of memory obtained from the OS",
	})

	// GC
	GoGCPauseDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "interlink_go_gc_pause_seconds",
		Help:    "GC stop-the-world pause duration",
		Buckets: []float64{0.00001, 0.00005, 0.0001, 0.0005, 0.001, 0.005, 0.01},
	})

	GoGCRunsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "interlink_go_gc_runs_total",
		Help: "Total number of completed GC cycles",
	})

	// Runtime
	GoGoroutines = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "interlink_go_goroutines",
		Help: "Number of active goroutines",
	})

	UptimeSeconds = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "interlink_uptime_seconds",
		Help: "Gateway uptime in seconds",
	})

	NATSReconnections = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "interlink_nats_reconnections_total",
		Help: "Total NATS reconnection events",
	})
)

// ── Throughput Counters ─────────────────────────────────

// adapterCounts tracks per-adapter message counts for the /health endpoint.
var adapterCounts sync.Map // map[string]*[2]atomic.Int64  — [0]=received, [1]=published

type adapterCounter struct {
	Received  atomic.Int64
	Published atomic.Int64
}

func getAdapterCounter(adapter string) *adapterCounter {
	val, _ := adapterCounts.LoadOrStore(adapter, &adapterCounter{})
	return val.(*adapterCounter)
}

// RecordReceive increments the received message counter for an adapter.
func RecordReceive(adapter string) {
	MessagesReceived.WithLabelValues(adapter).Inc()
	getAdapterCounter(adapter).Received.Add(1)
}

// RecordPublish increments the published message counter for an adapter.
func RecordPublish(adapter string) {
	MessagesPublished.WithLabelValues(adapter).Inc()
	getAdapterCounter(adapter).Published.Add(1)
}

// GetAdapterCounts returns received/published counts for the /health endpoint.
func GetAdapterCounts() map[string][2]int64 {
	result := make(map[string][2]int64)
	adapterCounts.Range(func(key, value any) bool {
		name := key.(string)
		c := value.(*adapterCounter)
		result[name] = [2]int64{c.Received.Load(), c.Published.Load()}
		return true
	})
	return result
}

// ── Init ────────────────────────────────────────────────

var startTime time.Time

func Init() {
	startTime = time.Now()
	prometheus.MustRegister(
		// Throughput
		MessagesPublished,
		MessagesReceived,
		// Latency
		MessageProcessingDuration,
		NATSPublishDuration,
		HTTPRequestDuration,
		PolicyEvalDuration,
		// Policy
		PolicyDecisions,
		PolicyDroppedTotal,
		// SSE
		SSEClientsActive,
		SSEDroppedMessagesTotal,
		// Registry
		RegistryDevices,
		// Storage
		SQLiteDBSizeBytes,
		DeadLettersTotal,
		// Memory
		GoHeapAllocBytes,
		GoHeapInuseBytes,
		GoStackInuseBytes,
		GoSysBytes,
		// GC
		GoGCPauseDuration,
		GoGCRunsTotal,
		// Runtime
		GoGoroutines,
		UptimeSeconds,
		NATSReconnections,
	)
}

// ── Runtime Stats Collector ─────────────────────────────

// DBStatsProvider is implemented by the registry to supply storage stats.
type DBStatsProvider interface {
	DeadLetterCount() (int, error)
	DBPath() string
}

// StartCollector runs a background goroutine that refreshes runtime metrics.
func StartCollector(ctx context.Context, dbStats DBStatsProvider, interval time.Duration) {
	var lastNumGC uint32
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				collectRuntime(&lastNumGC)
				collectStorage(dbStats)
			}
		}
	}()
	slog.Info("metrics collector started", "component", "metrics", "interval", interval.String())
}

func collectRuntime(lastNumGC *uint32) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	GoHeapAllocBytes.Set(float64(m.HeapAlloc))
	GoHeapInuseBytes.Set(float64(m.HeapInuse))
	GoStackInuseBytes.Set(float64(m.StackInuse))
	GoSysBytes.Set(float64(m.Sys))
	GoGoroutines.Set(float64(runtime.NumGoroutine()))
	UptimeSeconds.Set(time.Since(startTime).Seconds())

	// Record new GC pauses since last collection.
	newGCs := m.NumGC - *lastNumGC
	if newGCs > 0 {
		if newGCs > 256 {
			newGCs = 256 // PauseNs is a circular buffer of 256
		}
		for i := uint32(0); i < newGCs; i++ {
			idx := (m.NumGC - newGCs + i + 1) % 256
			GoGCPauseDuration.Observe(float64(m.PauseNs[idx]) / 1e9)
		}
		GoGCRunsTotal.Add(float64(newGCs))
		*lastNumGC = m.NumGC
	}
}

func collectStorage(dbStats DBStatsProvider) {
	if dbStats == nil {
		return
	}
	// DB file size
	if info, err := os.Stat(dbStats.DBPath()); err == nil {
		SQLiteDBSizeBytes.Set(float64(info.Size()))
	}
	// Dead letter count
	if count, err := dbStats.DeadLetterCount(); err == nil {
		DeadLettersTotal.Set(float64(count))
	}
}

// ── Health Snapshot ─────────────────────────────────────

// RuntimeSnapshot returns current runtime stats for the /health endpoint.
func RuntimeSnapshot() map[string]any {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	return map[string]any{
		"memory": map[string]any{
			"heap_alloc_mb":  float64(m.HeapAlloc) / 1024 / 1024,
			"heap_inuse_mb":  float64(m.HeapInuse) / 1024 / 1024,
			"stack_inuse_mb": float64(m.StackInuse) / 1024 / 1024,
			"sys_mb":         float64(m.Sys) / 1024 / 1024,
			"gc_pause_ms":    float64(m.PauseNs[(m.NumGC+255)%256]) / 1e6,
			"gc_runs":        m.NumGC,
		},
		"runtime": map[string]any{
			"goroutines": runtime.NumGoroutine(),
			"go_version": runtime.Version(),
		},
	}
}

// StorageSnapshot returns storage stats for the /health endpoint.
func StorageSnapshot(dbStats DBStatsProvider) map[string]any {
	result := map[string]any{}
	if dbStats == nil {
		return result
	}
	if info, err := os.Stat(dbStats.DBPath()); err == nil {
		result["db_size_mb"] = float64(info.Size()) / 1024 / 1024
	}
	if count, err := dbStats.DeadLetterCount(); err == nil {
		result["dead_letters"] = count
	}
	return result
}

// ThroughputSnapshot returns per-adapter message counts for the /health endpoint.
func ThroughputSnapshot() map[string]any {
	result := map[string]any{}
	var totalPublished int64
	adapterCounts.Range(func(key, value any) bool {
		name := key.(string)
		c := value.(*adapterCounter)
		result[name+"_received"] = c.Received.Load()
		result[name+"_published"] = c.Published.Load()
		totalPublished += c.Published.Load()
		return true
	})
	result["total_published"] = totalPublished
	return result
}

// Uptime returns server uptime.
func Uptime() time.Duration {
	return time.Since(startTime)
}
