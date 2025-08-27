package superblock

import (
	"time"

	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/l1"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/queue"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/slot"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/store"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/wal"
)

// Config aggregates configuration for all SBCP components
type Config struct {
	Slot  slot.Config  `mapstructure:"slot"  yaml:"slot"`
	Queue queue.Config `mapstructure:"queue" yaml:"queue"`
	Store store.Config `mapstructure:"store" yaml:"store"`
	WAL   wal.Config   `mapstructure:"wal"   yaml:"wal"`
	L1    l1.Config    `mapstructure:"l1"    yaml:"l1"`

	// Coordinator-level settings
	MaxConcurrentSlots     int           `mapstructure:"max_concurrent_slots"     yaml:"max_concurrent_slots"`
	BlockValidationTimeout time.Duration `mapstructure:"block_validation_timeout" yaml:"block_validation_timeout"`
}

// DefaultConfig returns sensible defaults for production deployment
func DefaultConfig() Config {
	return Config{
		Slot:  slot.DefaultConfig(),
		Queue: queue.DefaultConfig(),
		Store: store.DefaultConfig(),
		WAL:   wal.DefaultConfig(),
		L1:    l1.DefaultConfig(),

		MaxConcurrentSlots:     2,
		BlockValidationTimeout: 5 * time.Second,
	}
}
