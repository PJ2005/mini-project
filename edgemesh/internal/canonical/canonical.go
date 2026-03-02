package canonical

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
)

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

func Marshal(m *Message) ([]byte, error) {
	return proto.Marshal(m)
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
