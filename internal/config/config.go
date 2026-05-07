package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Serial Serial `toml:"serial"`
	Server Server `toml:"server"`
	UI     UI     `toml:"ui"`
}

type Serial struct {
	Port           string `toml:"port"`
	Baud           int    `toml:"baud"`
	PollIntervalMs int    `toml:"poll_interval_ms"`
}

type Server struct {
	Listen       string `toml:"listen"`
	HeartbeatMs  int    `toml:"heartbeat_ms"`
	MaxClients   int    `toml:"max_clients"`
	AllowControl bool   `toml:"allow_control"`
}

// UI controls which views the web client offers in its VIEW-pill cycle.
// This is independent of the meter's display mode: the Mode button on the
// web only cycles the meter's LCD; the VIEW pill cycles the web display.
type UI struct {
	EnabledViews []string `toml:"enabled_views"`
}

// ValidViews is the set of view names the web client supports rendering.
// All five of the LP-100A's main display modes are mirrored:
//   - normal         power + SWR bargraphs
//   - vector         |Z|, phase, R, X, with polar compass
//   - dbm            dBm + Return Loss (optional on meter, default off)
//   - field_strength dBm reading from Direct Input (optional, default off)
//   - peak_avg       peak/average ratio (optional, default off; client-side rolling window)
var ValidViews = map[string]bool{
	"normal":         true,
	"vector":         true,
	"dbm":            true,
	"field_strength": true,
	"peak_avg":       true,
}

func defaults() Config {
	return Config{
		Serial: Serial{
			Port:           "auto",
			Baud:           115200,
			PollIntervalMs: 100,
		},
		Server: Server{
			Listen:       "0.0.0.0:8088",
			HeartbeatMs:  2000,
			MaxClients:   32,
			AllowControl: true,
		},
		UI: UI{
			EnabledViews: []string{"normal", "vector", "dbm", "field_strength", "peak_avg"},
		},
	}
}

func Load(path string) (Config, error) {
	cfg := defaults()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if c.Serial.Baud <= 0 {
		return fmt.Errorf("serial.baud must be > 0")
	}
	if c.Serial.PollIntervalMs < 20 || c.Serial.PollIntervalMs > 1000 {
		return fmt.Errorf("serial.poll_interval_ms out of range [20,1000]")
	}
	if c.Server.Listen == "" {
		return fmt.Errorf("server.listen must be set")
	}
	if c.Server.MaxClients <= 0 {
		return fmt.Errorf("server.max_clients must be > 0")
	}
	if c.Server.HeartbeatMs < 200 {
		return fmt.Errorf("server.heartbeat_ms must be >= 200")
	}
	if len(c.UI.EnabledViews) == 0 {
		return fmt.Errorf("ui.enabled_views must list at least one view")
	}
	for _, v := range c.UI.EnabledViews {
		if !ValidViews[v] {
			return fmt.Errorf("ui.enabled_views: unknown view %q (valid: normal, vector, dbm, field_strength, peak_avg)", v)
		}
	}
	return nil
}
