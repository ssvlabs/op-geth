package store

// Config holds storage configuration
type Config struct {
	Engine string `mapstructure:"engine" yaml:"engine"`
	Path   string `mapstructure:"path"   yaml:"path"`
}

func DefaultConfig() Config {
	return Config{
		Engine: "badger",
		Path:   "./data/superblock",
	}
}
