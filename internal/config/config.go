package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

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

type Scratch struct {
	Dir      string `yaml:"dir"`
	MaxBytes Size   `yaml:"max_bytes"`
}

type Nice struct {
	CPU     int    `yaml:"cpu"`
	IOClass string `yaml:"io_class"` // idle | best-effort | none
}

type Server struct {
	Listen        string `yaml:"listen"`
	BasicAuthFile string `yaml:"basic_auth_file"` // bcrypt htpasswd path
	BasicAuthEnv  string `yaml:"basic_auth_env"`  // env var with plaintext user:password lines
	APIKeysEnv    string `yaml:"api_keys_env"`    // env var with bearer API keys
	AuthScope     string `yaml:"auth_scope"`      // api (default) | all
	AllowNoAuth   bool   `yaml:"allow_no_auth"`   // opt in to serving HTTP with no auth
}

func (s Server) hasAuth() bool {
	return s.BasicAuthFile != "" || s.BasicAuthEnv != "" || s.APIKeysEnv != ""
}

type Notify struct {
	URLs            []string `yaml:"urls"`
	Events          []string `yaml:"events"`
	HealthchecksURL string   `yaml:"healthchecks_url"`
}

var notifyEvents = map[string]bool{
	"fail": true, "error": true, "recover": true, "stale": true, "weekly_digest": true,
}

var ioClasses = map[string]bool{"idle": true, "best-effort": true, "none": true}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: config path is operator-supplied via -c, by design
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return Parse(data)
}

// Parse validates config bytes; unknown keys are errors.
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

// Validate checks semantic and cross-field rules; structural strictness is
// enforced earlier, during parsing.
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
	if c.Server.Listen != "" {
		if _, _, err := net.SplitHostPort(c.Server.Listen); err != nil {
			es.add("server.listen", "invalid listen address %q", c.Server.Listen)
		}
	}
	switch c.Server.AuthScope {
	case "", "api", "all":
	default:
		es.add("server.auth_scope", "must be api or all, got %q", c.Server.AuthScope)
	}
	if c.Server.AuthScope == "all" && !c.Server.hasAuth() {
		es.add("server.auth_scope", "all requires basic_auth_file, basic_auth_env, or api_keys_env")
	}
	// Secure by default: serving HTTP without any auth must be an explicit choice.
	if c.Server.Listen != "" && !c.Server.hasAuth() && !c.Server.AllowNoAuth {
		es.add("server.listen", "set without authentication; configure basic_auth_env, basic_auth_file, or api_keys_env, or set allow_no_auth: true to serve open (e.g. a private host or behind an authenticating reverse proxy)")
	}
	if u := c.Notify.HealthchecksURL; u != "" {
		if parsed, err := url.Parse(u); err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			es.add("notify.healthchecks_url", "must be an http(s) URL, got %q", u)
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
