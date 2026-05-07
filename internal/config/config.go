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

// UI controls which web views are part of the Mode-button cycle. The
// list MUST match (in order) the modes the meter cycles through in its
// "Opt. Mode Display" SETUP screen, otherwise the web view and the
// meter LCD will drift apart after a few presses. Default enables all
// five; trim the list (or re-order it) to match a non-default meter
// configuration. The setup overlay in the web UI provides a one-click
// re-alignment picker for handling initial-state mismatch.
type UI struct {
	EnabledViews []string `toml:"enabled_views"`
}

// ValidViews is the set of view names the web client supports rendering.
// Only the LP-100A's two always-on display modes are mirrored:
//   - normal   power + SWR bargraphs
//   - vector   |Z|, phase, R, X, with polar compass
//
// dBm/RL, Direct Input (Field Strength), and Peak-to-Avg are not
// included — the meter's serial protocol does not reliably report
// the values those views need (Power and dBm fields can read 0 / noise
// floor over serial even while the LCD shows real measurements).
var ValidViews = map[string]bool{
	"normal": true,
	"vector": true,
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
			EnabledViews: []string{"normal", "vector"},
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
			return fmt.Errorf("ui.enabled_views: unknown view %q (valid: normal, vector)", v)
		}
	}
	return nil
}
