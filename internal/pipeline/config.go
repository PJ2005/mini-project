package pipeline

import "time"

type PipelineConfig struct {
	Name       string            `yaml:"name"`
	Source     SourceConfig      `yaml:"source"`
	Transforms []TransformConfig `yaml:"transforms"`
	Sink       SinkConfig        `yaml:"sink"`
}

type SourceConfig struct {
	Type          string `yaml:"type"`
	Topic         string `yaml:"topic"`
	DeviceIDIndex int    `yaml:"device_id_index"`
	Subject       string `yaml:"subject"`
}

type TransformConfig struct {
	Type   string  `yaml:"type"`
	Field  string  `yaml:"field"`
	As     string  `yaml:"as"`
	From   string  `yaml:"from"`
	To     string  `yaml:"to"`
	Factor float64 `yaml:"factor"`
	Offset float64 `yaml:"offset"`
	Min    float64 `yaml:"min"`
	Max    float64 `yaml:"max"`
}

type SinkConfig struct {
	Type    string        `yaml:"type"`
	URL     string        `yaml:"url"`
	Subject string        `yaml:"subject"`
	Topic   string        `yaml:"topic"`
	Timeout time.Duration `yaml:"timeout"`
}
