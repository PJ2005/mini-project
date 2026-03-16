package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
)

type Config struct {
	Broker       string
	Topic        string
	Messages     int
	Rate         int // messages per second (0 = unlimited)
	Concurrency  int
	HealthURL    string
	WarmupSec    int
}

type Result struct {
	Config       ConfigReport   `json:"config"`
	Throughput   ThroughputR    `json:"throughput"`
	Latency      LatencyR       `json:"latency"`
	Health       HealthSnapshot `json:"health_after"`
	Errors       int            `json:"errors"`
	DroppedMsgs  int64          `json:"dropped_messages"`
}

type ConfigReport struct {
	Messages    int    `json:"total_messages"`
	Rate        int    `json:"target_rate_per_sec"`
	Concurrency int    `json:"concurrency"`
	Broker      string `json:"broker"`
}

type ThroughputR struct {
	TotalSent     int     `json:"total_sent"`
	TotalErrors   int     `json:"total_errors"`
	DurationSec   float64 `json:"duration_seconds"`
	ActualRatePS  float64 `json:"actual_rate_per_sec"`
}

type LatencyR struct {
	MinMs  float64 `json:"min_ms"`
	MaxMs  float64 `json:"max_ms"`
	MeanMs float64 `json:"mean_ms"`
	P50Ms  float64 `json:"p50_ms"`
	P95Ms  float64 `json:"p95_ms"`
	P99Ms  float64 `json:"p99_ms"`
}

type HealthSnapshot map[string]any

func main() {
	cfg := Config{}
	flag.StringVar(&cfg.Broker, "broker", "tcp://127.0.0.1:1883", "MQTT broker URL")
	flag.StringVar(&cfg.Topic, "topic", "devices/loadtest-", "MQTT topic prefix (device index appended)")
	flag.IntVar(&cfg.Messages, "messages", 1000, "Total messages to send")
	flag.IntVar(&cfg.Rate, "rate", 100, "Messages per second (0 = unlimited)")
	flag.IntVar(&cfg.Concurrency, "concurrency", 4, "Number of concurrent publishers")
	flag.StringVar(&cfg.HealthURL, "health", "http://127.0.0.1:8080/health", "InterLink /health URL (empty to skip)")
	flag.IntVar(&cfg.WarmupSec, "warmup", 2, "Seconds to wait after connect before starting")
	flag.Parse()

	fmt.Printf("InterLink Load Test\n")
	fmt.Printf("  Broker:      %s\n", cfg.Broker)
	fmt.Printf("  Messages:    %d\n", cfg.Messages)
	fmt.Printf("  Rate:        %d msg/s\n", cfg.Rate)
	fmt.Printf("  Concurrency: %d\n", cfg.Concurrency)
	fmt.Println()

	// Connect MQTT clients
	clients := make([]pahomqtt.Client, cfg.Concurrency)
	for i := 0; i < cfg.Concurrency; i++ {
		opts := pahomqtt.NewClientOptions().
			AddBroker(cfg.Broker).
			SetClientID(fmt.Sprintf("loadtest-%d-%d", os.Getpid(), i)).
			SetAutoReconnect(true)
		c := pahomqtt.NewClient(opts)
		tok := c.Connect()
		if !tok.WaitTimeout(5 * time.Second) {
			fmt.Fprintf(os.Stderr, "MQTT connect timeout for client %d\n", i)
			os.Exit(1)
		}
		if tok.Error() != nil {
			fmt.Fprintf(os.Stderr, "MQTT connect error: %v\n", tok.Error())
			os.Exit(1)
		}
		clients[i] = c
	}
	defer func() {
		for _, c := range clients {
			c.Disconnect(250)
		}
	}()

	fmt.Printf("Connected %d MQTT clients. Warming up %ds...\n", cfg.Concurrency, cfg.WarmupSec)
	time.Sleep(time.Duration(cfg.WarmupSec) * time.Second)

	// Run load test
	latencies := make([]float64, 0, cfg.Messages)
	var latMu sync.Mutex
	var errCount int64
	var sent int64

	msgsPerWorker := cfg.Messages / cfg.Concurrency
	intervalNs := int64(0)
	if cfg.Rate > 0 {
		intervalNs = int64(time.Second) / int64(cfg.Rate) * int64(cfg.Concurrency)
	}

	var wg sync.WaitGroup
	start := time.Now()

	for w := 0; w < cfg.Concurrency; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			client := clients[workerID]
			deviceID := fmt.Sprintf("loadtest-%d", workerID)
			topic := cfg.Topic + deviceID

			for i := 0; i < msgsPerWorker; i++ {
				temp := 20.0 + rand.Float64()*15.0
				payload := fmt.Sprintf(`{"temperature": %.2f}`, temp)

				pubStart := time.Now()
				tok := client.Publish(topic, 1, false, payload)
				tok.Wait()
				pubDur := time.Since(pubStart)

				if tok.Error() != nil {
					atomic.AddInt64(&errCount, 1)
				} else {
					latMu.Lock()
					latencies = append(latencies, float64(pubDur.Microseconds())/1000.0)
					latMu.Unlock()
					atomic.AddInt64(&sent, 1)
				}

				// Rate limiting
				if intervalNs > 0 {
					elapsed := time.Since(pubStart)
					sleep := time.Duration(intervalNs) - elapsed
					if sleep > 0 {
						time.Sleep(sleep)
					}
				}
			}
		}(w)
	}

	wg.Wait()
	totalDur := time.Since(start)

	fmt.Printf("\nLoad test complete. %d messages in %.2fs\n", sent, totalDur.Seconds())

	// Wait a few seconds for pipeline to drain
	fmt.Println("Waiting 3s for pipeline to drain...")
	time.Sleep(3 * time.Second)

	// Calculate latency percentiles
	sort.Float64s(latencies)
	latResult := LatencyR{}
	if len(latencies) > 0 {
		latResult.MinMs = latencies[0]
		latResult.MaxMs = latencies[len(latencies)-1]
		latResult.MeanMs = mean(latencies)
		latResult.P50Ms = percentile(latencies, 50)
		latResult.P95Ms = percentile(latencies, 95)
		latResult.P99Ms = percentile(latencies, 99)
	}

	// Fetch health endpoint
	var healthSnap HealthSnapshot
	if cfg.HealthURL != "" {
		healthSnap = fetchHealth(cfg.HealthURL)
	}

	// Build result
	result := Result{
		Config: ConfigReport{
			Messages:    cfg.Messages,
			Rate:        cfg.Rate,
			Concurrency: cfg.Concurrency,
			Broker:      cfg.Broker,
		},
		Throughput: ThroughputR{
			TotalSent:    int(sent),
			TotalErrors:  int(errCount),
			DurationSec:  math.Round(totalDur.Seconds()*100) / 100,
			ActualRatePS: math.Round(float64(sent)/totalDur.Seconds()*100) / 100,
		},
		Latency: latResult,
		Health:  healthSnap,
		Errors:  int(errCount),
	}

	// Output JSON
	out, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println("\n── BENCHMARK RESULTS ──────────────────────")
	fmt.Println(string(out))

	// Write to file
	outFile := fmt.Sprintf("benchmark_%s_%dmsg.json", time.Now().Format("20060102_150405"), cfg.Messages)
	os.WriteFile(outFile, out, 0644)
	fmt.Printf("\nResults saved to %s\n", outFile)

	// Print summary table
	fmt.Println("\n── SUMMARY ────────────────────────────────")
	fmt.Printf("  Total messages  : %d sent, %d errors\n", sent, errCount)
	fmt.Printf("  Duration        : %.2f seconds\n", totalDur.Seconds())
	fmt.Printf("  Throughput      : %.0f msg/s\n", float64(sent)/totalDur.Seconds())
	fmt.Printf("  Latency (MQTT publish):\n")
	fmt.Printf("    Min  : %.3f ms\n", latResult.MinMs)
	fmt.Printf("    p50  : %.3f ms\n", latResult.P50Ms)
	fmt.Printf("    p95  : %.3f ms\n", latResult.P95Ms)
	fmt.Printf("    p99  : %.3f ms\n", latResult.P99Ms)
	fmt.Printf("    Max  : %.3f ms\n", latResult.MaxMs)

	if healthSnap != nil {
		if mem, ok := healthSnap["memory"].(map[string]any); ok {
			fmt.Printf("  Memory:\n")
			fmt.Printf("    Heap alloc : %.2f MB\n", toFloat(mem["heap_alloc_mb"]))
			fmt.Printf("    Sys total  : %.2f MB\n", toFloat(mem["sys_mb"]))
		}
		if rt, ok := healthSnap["runtime"].(map[string]any); ok {
			fmt.Printf("    Goroutines : %.0f\n", toFloat(rt["goroutines"]))
		}
	}

	_ = runtime.NumCPU()
}

func mean(data []float64) float64 {
	sum := 0.0
	for _, v := range data {
		sum += v
	}
	return sum / float64(len(data))
}

func percentile(sorted []float64, pct float64) float64 {
	idx := int(math.Ceil(pct/100.0*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func fetchHealth(url string) HealthSnapshot {
	resp, err := http.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not fetch %s: %v\n", url, err)
		return nil
	}
	defer resp.Body.Close()
	var h HealthSnapshot
	json.NewDecoder(resp.Body).Decode(&h)
	return h
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	default:
		return 0
	}
}
