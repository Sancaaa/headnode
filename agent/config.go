package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is what the agent remembers between runs. It is written once by
// Enroll and holds the secret, so the file is private to its owner.
type Config struct {
	Server      string `json:"server"`
	AgentID     string `json:"agent_id"`
	AgentSecret string `json:"agent_secret"`

	// Path is where this config was read from (never written into the file). The
	// file manager refuses to hand that file out (secret.go).
	Path string `json:"-"`
}

// LoadConfig reads the file that Enroll wrote.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config (did you run `headnode-agent enroll` first?): %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Server == "" || cfg.AgentID == "" || cfg.AgentSecret == "" {
		return Config{}, fmt.Errorf("%s is incomplete: server, agent_id and agent_secret are all required", path)
	}
	if cfg.Path, err = filepath.Abs(path); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func saveConfig(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	// 0600 = only the owner can read it. O_EXCL = fail if the file already
	// exists, so we never overwrite credentials by accident.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return f.Close()
}
