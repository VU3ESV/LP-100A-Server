package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Serial Serial `toml:"serial"`
	Server Server `toml:"server"`
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
	return nil
}
