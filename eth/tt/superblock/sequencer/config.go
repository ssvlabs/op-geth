package sequencer

import (
	"time"

	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/slot"
)

// Config holds sequencer coordinator configuration
type Config struct {
	ChainID []byte      `json:"chain_id"`
	Slot    slot.Config `json:"slot"`

	// Sequencer-specific settings
	BlockTimeout         time.Duration `json:"block_timeout"`
	MaxLocalTxs          int           `json:"max_local_txs"`
	SCPTimeout           time.Duration `json:"scp_timeout"`
	EnableCIRCValidation bool          `json:"enable_circ_validation"`
}

// DefaultConfig returns sensible defaults for sequencer
func DefaultConfig(chainID []byte) Config {
	return Config{
		ChainID: chainID,
		Slot:    slot.DefaultConfig(),

		BlockTimeout:         30 * time.Second,
		MaxLocalTxs:          1000,
		SCPTimeout:           10 * time.Second,
		EnableCIRCValidation: true,
	}
}
