package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Config is the whole drillbit configuration (DESIGN §7). Load it with Load or
// Parse; both return a fully validated Config or a config error.
type Config struct {
	Version        int      `yaml:"version"`
	DataDir        string   `yaml:"data_dir"`
	Scratch        Scratch  `yaml:"scratch"`
	Concurrency    int      `yaml:"concurrency"`
	BandwidthLimit Size     `yaml:"bandwidth_limit"`
	Nice           Nice     `yaml:"nice"`
	Server         Server   `yaml:"server"`
	Notify         Notify   `yaml:"notify"`
	Sources        []Source `yaml:"sources"`
	Drills         []Drill  `yaml:"drills"`
}

// Scratch is the quota-managed temp space restores land in.
type Scratch struct {
	Dir      string `yaml:"dir"`
	MaxBytes Size   `yaml:"max_bytes"`
}

// Nice tunes the scheduling priority applied to spawned engine processes.
type Nice struct {
	CPU     int    `yaml:"cpu"`
	IOClass string `yaml:"io_class"` // idle | best-effort | none
}

// Server holds the (Phase 2) HTTP listener configuration.
type Server struct {
	Listen        string `yaml:"listen"`
	BasicAuthFile string `yaml:"basic_auth_file"`
}

// Notify routes events to shoutrrr URLs.
type Notify struct {
	URLs            []string `yaml:"urls"`
	Events          []string `yaml:"events"`
	HealthchecksURL string   `yaml:"healthchecks_url"`
}

var notifyEvents = map[string]bool{
	"fail": true, "error": true, "recover": true, "stale": true, "weekly_digest": true,
}

var ioClasses = map[string]bool{"idle": true, "best-effort": true, "none": true}

// Load reads, parses, and validates the config at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: config path is operator-supplied via -c, by design
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return Parse(data)
}

// Parse parses and validates config bytes. Unknown keys are errors.
func Parse(data []byte) (*Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Concurrency == 0 {
		c.Concurrency = 1
	}
	for i := range c.Sources {
		if c.Sources[i].Type == "dumpdir" && c.Sources[i].Pick == "" {
			c.Sources[i].Pick = "newest"
		}
	}
	for i := range c.Drills {
		if l3 := c.Drills[i].Levels.L3; l3 != nil {
			if l3.Load == "" {
				l3.Load = "auto"
			}
			if l3.Sandbox.Network == "" {
				l3.Sandbox.Network = "none"
			}
		}
	}
}

// Validate checks semantic and cross-field rules. Structural strictness
// (unknown keys, bad durations/sizes) is enforced earlier, during parsing.
func (c *Config) Validate() error {
	var es errset
	if c.Version != 1 {
		es.add("version", "must be 1, got %d", c.Version)
	}
	if c.DataDir == "" {
		es.add("data_dir", "required")
	}
	if c.Scratch.Dir == "" {
		es.add("scratch.dir", "required")
	}
	if c.Concurrency < 1 {
		es.add("concurrency", "must be >= 1, got %d", c.Concurrency)
	}
	if c.Nice.IOClass != "" && !ioClasses[c.Nice.IOClass] {
		es.add("nice.io_class", "must be idle, best-effort, or none, got %q", c.Nice.IOClass)
	}
	for i, ev := range c.Notify.Events {
		if !notifyEvents[ev] {
			es.add(fmt.Sprintf("notify.events[%d]", i), "unknown event %q", ev)
		}
	}

	srcType := map[string]string{}
	seen := map[string]bool{}
	for i := range c.Sources {
		s := &c.Sources[i]
		p := fmt.Sprintf("sources[%d]", i)
		switch {
		case s.Name == "":
			es.add(p+".name", "required")
		case seen[s.Name]:
			es.add(p+".name", "duplicate source name %q", s.Name)
		default:
			seen[s.Name] = true
			srcType[s.Name] = s.Type
		}
		s.validate(p, &es)
	}

	dseen := map[string]bool{}
	for i := range c.Drills {
		d := &c.Drills[i]
		p := fmt.Sprintf("drills[%d]", i)
		switch {
		case d.Name == "":
			es.add(p+".name", "required")
		case dseen[d.Name]:
			es.add(p+".name", "duplicate drill name %q", d.Name)
		default:
			dseen[d.Name] = true
		}
		t, ok := srcType[d.Source]
		switch {
		case d.Source == "":
			es.add(p+".source", "required")
		case !ok:
			es.add(p+".source", "no such source %q", d.Source)
		}
		d.validate(p, t, &es)
	}
	return es.err()
}

// ValidationError is one semantic problem, qualified by its path in the config.
type ValidationError struct {
	Path string
	Msg  string
}

func (e *ValidationError) Error() string { return e.Path + ": " + e.Msg }

type errset struct{ errs []error }

func (s *errset) add(path, format string, args ...any) {
	s.errs = append(s.errs, &ValidationError{Path: path, Msg: fmt.Sprintf(format, args...)})
}

func (s *errset) err() error { return errors.Join(s.errs...) }

func itoa(i int) string { return strconv.Itoa(i) }
