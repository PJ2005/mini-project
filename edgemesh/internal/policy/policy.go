package policy

import (
	"log"
	"path/filepath"

	"edgemesh/internal/canonical"
)

type Rule struct {
	DevicePattern string `yaml:"device_pattern"`
	SourceProto   string `yaml:"source_proto"`
	MessageType   string `yaml:"message_type"`
	Action        string `yaml:"action"`
}

type Config struct {
	DefaultAction string `yaml:"default_action"`
	Rules         []Rule `yaml:"rules"`
}

type Engine struct {
	rules         []Rule
	defaultAction string
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

// Evaluate returns true if the message should be forwarded.
func (e *Engine) Evaluate(msg *canonical.Message) bool {
	msgType := canonical.SubjectType(msg)
	deviceID := msg.GetDeviceId()
	sourceProto := msg.GetSourceProto()

	for _, r := range e.rules {
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
		if !allowed {
			log.Printf("[policy] denied: device=%s type=%s source=%s", deviceID, msgType, sourceProto)
		}
		return allowed
	}

	return e.defaultAction == "allow"
}

// matchField returns true if the pattern is empty (wildcard), "*", or
// matches the value using filepath.Match glob syntax.
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
