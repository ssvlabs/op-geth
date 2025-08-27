package queue

import "time"

// Config holds transaction queue configuration
type Config struct {
	MaxSize           int           `mapstructure:"max_size"           yaml:"max_size"`
	RequestExpiration time.Duration `mapstructure:"request_expiration" yaml:"request_expiration"`
}

func DefaultConfig() Config {
	return Config{
		MaxSize:           10000,
		RequestExpiration: 5 * time.Minute,
	}
}
