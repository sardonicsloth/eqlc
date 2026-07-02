package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config is persisted to the OS config dir so settings survive restarts:
//   Linux:   ~/.config/eqdps/config.json
//   macOS:   ~/Library/Application Support/eqdps/config.json
//   Windows: %AppData%\eqdps\config.json
type Config struct {
	LogPath  string  `json:"log"`
	Player   string  `json:"player"`
	Gap      float64 `json:"gap"`
	MineOnly bool    `json:"mine_only"`
	OnTop    bool    `json:"on_top"`
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "eqdps", "config.json")
}

func loadConfig() *Config {
	c := &Config{Gap: 10}
	p := configPath()
	if p == "" {
		return c
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return c
	}
	_ = json.Unmarshal(b, c)
	if c.Gap <= 0 {
		c.Gap = 10
	}
	return c
}

func (c *Config) save() {
	p := configPath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if b, err := json.MarshalIndent(c, "", "  "); err == nil {
		_ = os.WriteFile(p, b, 0o644)
	}
}
