package canonical

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
)

// marshalBufPool reduces GC pressure by reusing byte slices for marshaling.
var marshalBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 512)
		return &b
	},
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func nowMs() int64 {
	return time.Now().UnixMilli()
}

func NewTelemetryMessage(deviceID, sourceProto, metric string, value float64, unit string) *Message {
	return &Message{
		MessageId:   generateID(),
		DeviceId:    deviceID,
		TimestampMs: nowMs(),
		SourceProto: sourceProto,
		Payload: &Message_Telemetry{
			Telemetry: &TelemetryPayload{
				Metric: metric,
				Value:  value,
				Unit:   unit,
			},
		},
	}
}

func NewEventMessage(deviceID, sourceProto, eventType, severity, detail string) *Message {
	return &Message{
		MessageId:   generateID(),
		DeviceId:    deviceID,
		TimestampMs: nowMs(),
		SourceProto: sourceProto,
		Payload: &Message_Event{
			Event: &EventPayload{
				EventType: eventType,
				Severity:  severity,
				Detail:    detail,
			},
		},
	}
}

func NewCommandMessage(deviceID, sourceProto, action string, params []byte) *Message {
	return &Message{
		MessageId:   generateID(),
		DeviceId:    deviceID,
		TimestampMs: nowMs(),
		SourceProto: sourceProto,
		Payload: &Message_Command{
			Command: &CommandPayload{
				Action: action,
				Params: params,
			},
		},
	}
}

// Marshal serializes a canonical Message to Protobuf wire format.
// Internally uses a sync.Pool to reduce per-message allocation overhead.
func Marshal(m *Message) ([]byte, error) {
	bp := marshalBufPool.Get().(*[]byte)
	buf := (*bp)[:0]

	opts := proto.MarshalOptions{}
	var err error
	buf, err = opts.MarshalAppend(buf, m)
	if err != nil {
		*bp = buf
		marshalBufPool.Put(bp)
		return nil, err
	}

	// Return a copy so the caller owns the bytes; recycle the pooled slice.
	out := make([]byte, len(buf))
	copy(out, buf)
	*bp = buf
	marshalBufPool.Put(bp)
	return out, nil
}

// MarshalPooled serializes a canonical Message using the shared marshal buffer pool.
func MarshalPooled(m *Message) ([]byte, error) {
	bp := marshalBufPool.Get().(*[]byte)
	buf := (*bp)[:0]

	opts := proto.MarshalOptions{}
	var err error
	buf, err = opts.MarshalAppend(buf, m)
	if err != nil {
		*bp = buf
		marshalBufPool.Put(bp)
		return nil, err
	}

	out := make([]byte, len(buf))
	copy(out, buf)
	*bp = buf
	marshalBufPool.Put(bp)
	return out, nil
}

func UnmarshalMessage(data []byte) (*Message, error) {
	m := &Message{}
	if err := proto.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("unmarshal canonical message: %w", err)
	}
	return m, nil
}

// SubjectType returns the NATS subject type segment for this message.
func SubjectType(m *Message) string {
	switch m.Payload.(type) {
	case *Message_Telemetry:
		return "telemetry"
	case *Message_Command:
		return "command"
	case *Message_Event:
		return "event"
	default:
		return "unknown"
	}
}

// Subject returns the full NATS subject: iot.<type>.<device_id>
func Subject(m *Message) string {
	return fmt.Sprintf("iot.%s.%s", SubjectType(m), m.DeviceId)
}
