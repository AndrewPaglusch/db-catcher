package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"go.yaml.in/yaml/v3"
)

// engine describes what a complete dump from one database tool looks like.
type engine struct {
	marker string
}

var engines = map[string]engine{
	"postgres": {marker: "-- PostgreSQL database dump complete"},
	"mysql":    {marker: "-- Dump completed"},
}

type Config struct {
	Listen         string    `yaml:"listen"`
	TLSCert        string    `yaml:"tls_cert"`
	TLSKey         string    `yaml:"tls_key"`
	S3             S3Config  `yaml:"s3"`
	NotifyWebhook  string    `yaml:"notify_webhook"`
	AlertOnInvalid bool      `yaml:"alert_on_invalid"`
	Backups        []*Backup `yaml:"backups"`

	byToken map[[32]byte]*Backup
}

type S3Config struct {
	Endpoint string `yaml:"endpoint"`
	Region   string `yaml:"region"`
	Bucket   string `yaml:"bucket"`
}

type Backup struct {
	ID                string `yaml:"id"`
	Name              string `yaml:"name"`
	Type              string `yaml:"type"`
	Keep              int    `yaml:"keep"`
	AlertAfterMinutes int    `yaml:"alert_after_minutes"`
	NotifyWebhook     string `yaml:"notify_webhook"`
	AlertOnInvalidSet *bool  `yaml:"alert_on_invalid"`

	AlertOnInvalid bool `yaml:"-"`
	engine         engine
}

func (b *Backup) alertAfter() time.Duration {
	return time.Duration(b.AlertAfterMinutes) * time.Minute
}

// backupFor returns the backup whose ID is token, or nil. Looking up by hash
// keeps the comparison time independent of how much of the token matched.
func (c *Config) backupFor(token string) *Backup {
	return c.byToken[sha256.Sum256([]byte(token))]
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := parseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func parseConfig(data []byte) (*Config, error) {
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	return &cfg, nil
}

var (
	uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	namePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

func (c *Config) validate() error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if (c.TLSCert == "") != (c.TLSKey == "") {
		fail("tls_cert and tls_key must be set together")
	}
	if c.S3.Region == "" {
		fail("s3.region is required")
	}
	if c.S3.Bucket == "" {
		fail("s3.bucket is required")
	}
	if len(c.Backups) == 0 {
		fail("no backups defined")
	}

	ids := map[string]bool{}
	names := map[string]bool{}
	for i, b := range c.Backups {
		label := fmt.Sprintf("backups[%d] %q", i, b.Name)

		switch {
		case !uuidV4Pattern.MatchString(b.ID):
			fail("%s: id must be a lowercase random (v4) UUID", label)
		case ids[b.ID]:
			fail("%s: id is used by another backup", label)
		}
		ids[b.ID] = true

		switch {
		case !namePattern.MatchString(b.Name):
			fail("%s: name may only contain letters, digits, '.', '_' and '-'", label)
		case names[b.Name]:
			fail("%s: name is used by another backup", label)
		}
		names[b.Name] = true

		if _, ok := engines[b.Type]; !ok {
			fail("%s: unknown type %q (want postgres or mysql)", label, b.Type)
		}
		if b.Keep <= 0 {
			fail("%s: keep must be at least 1", label)
		}
		if b.AlertAfterMinutes <= 0 {
			fail("%s: alert_after_minutes must be at least 1", label)
		}
		if b.NotifyWebhook == "" && c.NotifyWebhook == "" {
			fail("%s: no notify_webhook set, globally or on the backup", label)
		}
	}
	return errors.Join(errs...)
}

// applyDefaults fills in every inherited value so nothing past config loading
// has to know about global defaults or per-backup overrides.
func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	c.byToken = make(map[[32]byte]*Backup, len(c.Backups))
	for _, b := range c.Backups {
		b.engine = engines[b.Type]
		if b.NotifyWebhook == "" {
			b.NotifyWebhook = c.NotifyWebhook
		}
		b.AlertOnInvalid = c.AlertOnInvalid
		if b.AlertOnInvalidSet != nil {
			b.AlertOnInvalid = *b.AlertOnInvalidSet
		}
		c.byToken[sha256.Sum256([]byte(b.ID))] = b
	}
}
