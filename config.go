package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config is persisted to the OS config dir so settings survive restarts:
//   Linux:   ~/.config/eqlc/config.json
//   macOS:   ~/Library/Application Support/eqlc/config.json
//   Windows: %AppData%\eqlc\config.json
type Config struct {
	LogPath  string  `json:"log"`
	Player   string  `json:"player"`
	Gap      float64 `json:"gap"`
	MineOnly bool    `json:"mine_only"`
	OnTop    bool    `json:"on_top"`
	// SyncCmd, when set, adds a "Sync" button to the Inventory tab that runs
	// this shell command — useful when the game runs in a VM/Wine and a script
	// copies its files somewhere EQLC can read.
	SyncCmd string `json:"sync_cmd,omitempty"`
}

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "eqlc", "config.json")
}

// legacyConfigPath is the pre-rename (eqdps) location, read as a fallback.
func legacyConfigPath() string {
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
		if lp := legacyConfigPath(); lp != "" {
			b, err = os.ReadFile(lp)
		}
		if err != nil {
			return c
		}
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
