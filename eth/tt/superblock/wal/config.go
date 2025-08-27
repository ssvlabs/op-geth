package wal

import "time"

// Config holds write-ahead logging configuration
type Config struct {
	Enabled      bool          `mapstructure:"enabled"       yaml:"enabled"`
	Path         string        `mapstructure:"path"          yaml:"path"`
	SyncInterval time.Duration `mapstructure:"sync_interval" yaml:"sync_interval"` // How often to fsync WAL to disk
}

func DefaultConfig() Config {
	return Config{
		Enabled:      true,
		Path:         "./data/wal",
		SyncInterval: 100 * time.Millisecond,
	}
}
