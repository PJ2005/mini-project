package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pahomqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/udp"
	udpclient "github.com/plgd-dev/go-coap/v3/udp/client"
	"golang.org/x/net/websocket"
)

type report struct {
	Protocol     string         `json:"protocol"`
	TargetRate   int            `json:"target_rate"`
	ActualRate   float64        `json:"actual_rate"`
	TotalSent    int64          `json:"total_sent"`
	TotalDropped int64          `json:"total_dropped"`
	DurationS    float64        `json:"duration_s"`
	LatencyMS    latencySummary `json:"latency_ms"`
}

type latencySummary struct {
	Min float64 `json:"min"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

type sseMessage struct {
	DeviceID string            `json:"device_id"`
	Metadata map[string]string `json:"metadata"`
}

type sender interface {
	Send(ctx context.Context, payload []byte, deviceID string) error
	Close() error
}

func main() {
	protocol := flag.String("protocol", "mqtt", "mqtt|http|coap|websocket")
	rate := flag.Int("rate", 100, "messages per second")
	duration := flag.Int("duration", 60, "seconds to run")
	payloadInput := flag.String("payload", `{"metric":"load","value":1,"unit":"count"}`, "path to JSON file OR inline JSON string")
	target := flag.String("target", "tcp://127.0.0.1:1883", "broker/host:port")
	deviceID := flag.String("device-id", "loadgen-01", "device ID")
	reportPath := flag.String("report", "loadgen_report.json", "path to write JSON report")
	flag.Parse()

	basePayload, err := readPayload(*payloadInput)
	if err != nil {
		fatalf("payload parse error: %v", err)
	}

	s, err := newSender(*protocol, *target)
	if err != nil {
		fatalf("sender init error: %v", err)
	}
	defer s.Close()

	var latMu sync.Mutex
	latencies := make([]float64, 0, 1024)

	sseCtx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel()
	go streamSSE(sseCtx, *target, *deviceID, func(v float64) {
		latMu.Lock()
		latencies = append(latencies, v)
		latMu.Unlock()
	})

	interval := time.Second
	if *rate > 0 {
		interval = time.Second / time.Duration(*rate)
	}

	runCtx, cancel := context.WithTimeout(context.Background(), time.Duration(*duration)*time.Second)
	defer cancel()

	var sent int64
	var dropped int64
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	start := time.Now()

	for {
		select {
		case <-runCtx.Done():
			goto done
		case <-ticker.C:
			sendTS := time.Now().UnixNano()
			payload := payloadWithMetadata(basePayload, sendTS)
			data, err := json.Marshal(payload)
			if err != nil {
				atomic.AddInt64(&dropped, 1)
				continue
			}
			if err := s.Send(runCtx, data, *deviceID); err != nil {
				atomic.AddInt64(&dropped, 1)
				continue
			}
			atomic.AddInt64(&sent, 1)
		}
	}

done:
	elapsed := time.Since(start).Seconds()
	if elapsed <= 0 {
		elapsed = 0.001
	}

	latMu.Lock()
	summary := summarize(latencies)
	latMu.Unlock()

	out := report{
		Protocol:     strings.ToLower(*protocol),
		TargetRate:   *rate,
		ActualRate:   round(float64(sent)/elapsed, 2),
		TotalSent:    sent,
		TotalDropped: dropped,
		DurationS:    round(elapsed, 2),
		LatencyMS:    summary,
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fatalf("report marshal error: %v", err)
	}
	if err := os.WriteFile(*reportPath, data, 0o644); err != nil {
		fatalf("report write error: %v", err)
	}
	fmt.Println(string(data))
}

func readPayload(input string) (map[string]any, error) {
	if input == "" {
		return nil, errors.New("empty payload")
	}
	raw := []byte(input)
	if looksLikePath(input) {
		fileData, err := os.ReadFile(input)
		if err == nil {
			raw = fileData
		}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func looksLikePath(s string) bool {
	return strings.Contains(s, string(filepath.Separator)) || strings.HasSuffix(strings.ToLower(s), ".json")
}

func payloadWithMetadata(base map[string]any, sendTS int64) map[string]any {
	out := cloneMap(base)
	metadata := map[string]string{
		"send_ts_ns": strconv.FormatInt(sendTS, 10),
	}
	out["metadata"] = metadata
	return out
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func summarize(lat []float64) latencySummary {
	if len(lat) == 0 {
		return latencySummary{}
	}
	sort.Float64s(lat)
	return latencySummary{
		Min: round(lat[0], 3),
		P50: round(percentile(lat, 50), 3),
		P95: round(percentile(lat, 95), 3),
		P99: round(percentile(lat, 99), 3),
		Max: round(lat[len(lat)-1], 3),
	}
}

func percentile(sorted []float64, pct float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int((pct / 100.0) * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func round(v float64, places int) float64 {
	p := 1.0
	for i := 0; i < places; i++ {
		p *= 10
	}
	if v >= 0 {
		return float64(int(v*p+0.5)) / p
	}
	return float64(int(v*p-0.5)) / p
}

func streamSSE(ctx context.Context, target, deviceID string, onLatency func(float64)) {
	base := sseBaseURL(target)
	if base == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/stream", nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, resp.Body)
		return
	}

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		body := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		var msg sseMessage
		if err := json.Unmarshal([]byte(body), &msg); err != nil {
			continue
		}
		if msg.DeviceID != deviceID {
			continue
		}
		tsRaw := msg.Metadata["send_ts_ns"]
		if tsRaw == "" {
			continue
		}
		sendTS, err := strconv.ParseInt(tsRaw, 10, 64)
		if err != nil || sendTS <= 0 {
			continue
		}
		latencyMS := float64(time.Now().UnixNano()-sendTS) / 1e6
		if latencyMS >= 0 {
			onLatency(latencyMS)
		}
	}
}

func sseBaseURL(target string) string {
	t := strings.TrimSpace(target)
	if t == "" {
		return ""
	}
	if !strings.Contains(t, "://") {
		if strings.Contains(t, ":") {
			return "http://" + t
		}
		return "http://" + t + ":8080"
	}
	u, err := url.Parse(t)
	if err != nil || u.Host == "" {
		return ""
	}
	switch u.Scheme {
	case "http", "https":
		return strings.TrimRight(u.String(), "/")
	default:
		host := u.Hostname()
		if host == "" {
			return ""
		}
		return "http://" + host + ":8080"
	}
}

func newSender(protocol, target string) (sender, error) {
	switch strings.ToLower(protocol) {
	case "mqtt":
		return newMQTTSender(target)
	case "http":
		return newHTTPSender(target), nil
	case "coap":
		return newCoAPSender(target)
	case "websocket":
		return newWebSocketSender(target)
	default:
		return nil, fmt.Errorf("unsupported protocol %q", protocol)
	}
}

type mqttSender struct {
	client pahomqtt.Client
	target string
}

func newMQTTSender(target string) (sender, error) {
	broker := target
	if !strings.Contains(broker, "://") {
		broker = "tcp://" + broker
	}
	opts := pahomqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID(fmt.Sprintf("interlink-loadgen-%d", time.Now().UnixNano())).
		SetAutoReconnect(true)
	client := pahomqtt.NewClient(opts)
	token := client.Connect()
	if !token.WaitTimeout(5 * time.Second) {
		return nil, fmt.Errorf("mqtt connect timeout")
	}
	if err := token.Error(); err != nil {
		return nil, err
	}
	return &mqttSender{client: client, target: "devices"}, nil
}

func (s *mqttSender) Send(ctx context.Context, payload []byte, deviceID string) error {
	_ = ctx
	topic := fmt.Sprintf("%s/%s", s.target, deviceID)
	token := s.client.Publish(topic, 1, false, payload)
	token.Wait()
	return token.Error()
}

func (s *mqttSender) Close() error {
	if s.client != nil && s.client.IsConnected() {
		s.client.Disconnect(250)
	}
	return nil
}

type httpSender struct {
	base   string
	client *http.Client
}

func newHTTPSender(target string) sender {
	base := target
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	return &httpSender{base: strings.TrimRight(base, "/"), client: &http.Client{Timeout: 5 * time.Second}}
}

func (s *httpSender) Send(ctx context.Context, payload []byte, deviceID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/ingest/v1/"+deviceID+"/telemetry", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http status %d", resp.StatusCode)
	}
	return nil
}

func (s *httpSender) Close() error { return nil }

type coapSender struct {
	conn *udpclient.Conn
}

func newCoAPSender(target string) (sender, error) {
	addr := target
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil {
			return nil, err
		}
		addr = u.Host
	}
	conn, err := udp.Dial(addr)
	if err != nil {
		return nil, err
	}
	return &coapSender{conn: conn}, nil
}

func (s *coapSender) Send(ctx context.Context, payload []byte, deviceID string) error {
	resp, err := s.conn.Post(ctx, "/telemetry/"+deviceID, message.AppJSON, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if resp == nil {
		return errors.New("nil coap response")
	}
	return nil
}

func (s *coapSender) Close() error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

type wsSender struct {
	base string
	mu   sync.Mutex
	conn *websocket.Conn
}

func newWebSocketSender(target string) (sender, error) {
	base := target
	if !strings.Contains(base, "://") {
		base = "ws://" + base
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		u.Scheme = "ws"
	}
	return &wsSender{base: strings.TrimRight(u.String(), "/")}, nil
}

func (s *wsSender) Send(ctx context.Context, payload []byte, deviceID string) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		conn, err := websocket.Dial(s.base+"/ws/"+deviceID, "", "http://localhost/")
		if err != nil {
			return err
		}
		s.conn = conn
	}
	return websocket.Message.Send(s.conn, payload)
}

func (s *wsSender) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
