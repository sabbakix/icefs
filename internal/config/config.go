// Package config loads and saves the IceFs configuration file.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

const DefaultPath = "/etc/icefs/config.toml"

// Duration wraps time.Duration so it can be written as "10m" in TOML.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// Share is one backup folder watched by IceFs.
type Share struct {
	Name           string  `toml:"name" json:"name"`
	Path           string  `toml:"path" json:"path"`
	RetentionDays  int     `toml:"retention_days" json:"retention_days"`
	SettleMinutes  int     `toml:"settle_minutes" json:"settle_minutes"`
	MinFreePercent float64 `toml:"min_free_percent" json:"min_free_percent"`
	AutoDelete     bool    `toml:"auto_delete" json:"auto_delete"`
}

func (s Share) Retention() time.Duration {
	return time.Duration(s.RetentionDays) * 24 * time.Hour
}

func (s Share) Settle() time.Duration {
	return time.Duration(s.SettleMinutes) * time.Minute
}

type Config struct {
	DBPath       string   `toml:"db_path"`
	SocketPath   string   `toml:"socket_path"`
	ScanInterval Duration `toml:"scan_interval"`
	Shares       []Share  `toml:"share"`
}

// Defaults for anything left out of the file.
const (
	DefaultDBPath         = "/var/lib/icefs/state.db"
	DefaultSocketPath     = "/run/icefs/icefs.sock"
	DefaultScanInterval   = time.Minute
	DefaultRetentionDays  = 15
	DefaultSettleMinutes  = 15
	DefaultMinFreePercent = 15
)

func Load(path string) (*Config, error) {
	c := &Config{}
	if _, err := toml.DecodeFile(path, c); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c *Config) applyDefaults() {
	if c.DBPath == "" {
		c.DBPath = DefaultDBPath
	}
	if c.SocketPath == "" {
		c.SocketPath = DefaultSocketPath
	}
	if c.ScanInterval.Duration == 0 {
		c.ScanInterval.Duration = DefaultScanInterval
	}
	for i := range c.Shares {
		s := &c.Shares[i]
		if s.RetentionDays == 0 {
			s.RetentionDays = DefaultRetentionDays
		}
		if s.SettleMinutes == 0 {
			s.SettleMinutes = DefaultSettleMinutes
		}
		if s.MinFreePercent == 0 {
			s.MinFreePercent = DefaultMinFreePercent
		}
		s.Path = filepath.Clean(s.Path)
	}
}

func (c *Config) Validate() error {
	seen := map[string]bool{}
	for _, s := range c.Shares {
		if s.Name == "" {
			return fmt.Errorf("share with path %q has no name", s.Path)
		}
		if seen[s.Name] {
			return fmt.Errorf("share name %q is used twice", s.Name)
		}
		seen[s.Name] = true
		if !filepath.IsAbs(s.Path) {
			return fmt.Errorf("share %q: path must be absolute", s.Name)
		}
		if err := ValidateShareSettings(s); err != nil {
			return fmt.Errorf("share %q: %w", s.Name, err)
		}
	}
	return nil
}

// ValidateShareSettings checks the values a user can edit from the TUI.
func ValidateShareSettings(s Share) error {
	if s.RetentionDays < 1 || s.RetentionDays > 3650 {
		return fmt.Errorf("retention_days must be between 1 and 3650")
	}
	if s.SettleMinutes < 1 || s.SettleMinutes > 24*60 {
		return fmt.Errorf("settle_minutes must be between 1 and 1440")
	}
	if s.MinFreePercent <= 0 || s.MinFreePercent >= 100 {
		return fmt.Errorf("min_free_percent must be between 0 and 100")
	}
	return nil
}

// Share returns the share with the given name.
func (c *Config) Share(name string) (*Share, bool) {
	for i := range c.Shares {
		if c.Shares[i].Name == name {
			return &c.Shares[i], true
		}
	}
	return nil, false
}

// Save writes the config atomically.
func (c *Config) Save(path string) error {
	var buf bytes.Buffer
	buf.WriteString("# IceFs configuration. Share settings can also be changed from the TUI.\n\n")
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
