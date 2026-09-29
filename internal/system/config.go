// Package system holds process-level configuration, logging and installation.
//
// Only bootstrap settings live here (where the database is, which port to
// listen on). Everything else is configuration in SQLite.
package system

import (
	"fmt"
	"path/filepath"
)

// Config is the bootstrap configuration, read from NEXO_* environment variables.
type Config struct {
	DataDir   string // directory holding nexo.db
	Listen    string // admin UI listen address
	LogLevel  string
	LogFormat string
	KeyPath   string // master key file
	Hardware  string // "real" or "mock"
}

const (
	DefaultDataDir = "/var/lib/nexo"
	DefaultKeyPath = "/etc/nexo/master.key"
	credentialName = "master.key"
)

// LoadConfig builds a Config from the environment. getenv is os.Getenv in
// production and a stub in tests.
func LoadConfig(getenv func(string) string) (Config, error) {
	get := func(name, def string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return def
	}
	cfg := Config{
		DataDir:   get("NEXO_DATA_DIR", DefaultDataDir),
		Listen:    get("NEXO_LISTEN", ":8080"),
		LogLevel:  get("NEXO_LOG_LEVEL", "info"),
		LogFormat: get("NEXO_LOG_FORMAT", "text"),
		Hardware:  get("NEXO_HARDWARE", "real"),
	}
	switch {
	case getenv("NEXO_MASTER_KEY_FILE") != "":
		cfg.KeyPath = getenv("NEXO_MASTER_KEY_FILE")
	case getenv("CREDENTIALS_DIRECTORY") != "":
		// Provided by systemd LoadCredential=.
		cfg.KeyPath = filepath.Join(getenv("CREDENTIALS_DIRECTORY"), credentialName)
	default:
		cfg.KeyPath = DefaultKeyPath
	}
	if cfg.Hardware != "real" && cfg.Hardware != "mock" {
		return Config{}, fmt.Errorf("NEXO_HARDWARE must be \"real\" or \"mock\", got %q", cfg.Hardware)
	}
	return cfg, nil
}

// DBPath is the SQLite database file.
func (c Config) DBPath() string { return filepath.Join(c.DataDir, "nexo.db") }
