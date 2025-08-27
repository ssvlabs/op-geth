package superblock

import (
	"time"

	pb "github.com/ssvlabs/rollup-shared-publisher/proto/rollup/v1"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/queue"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/slot"
)

// SlotExecution tracks the execution state for a single slot in the SBCP
type SlotExecution struct {
	Slot                 uint64                            `json:"slot"`
	State                SlotState                         `json:"state"`
	StartTime            time.Time                         `json:"start_time"`
	NextSuperblockNumber uint64                            `json:"next_superblock_number"`
	LastSuperblockHash   []byte                            `json:"last_superblock_hash"`
	ActiveRollups        [][]byte                          `json:"active_rollups"`     // Rollups participating in slot
	ReceivedL2Blocks     map[string]*pb.L2Block            `json:"received_l2_blocks"` // Key: chainID
	SCPInstances         map[string]*slot.SCPInstance      `json:"scp_instances"`      // Key: xtID
	L2BlockRequests      map[string]*pb.L2BlockRequest     `json:"l2_block_requests"`  // Key: chainID
	AttemptedRequests    map[string]*queue.QueuedXTRequest `json:"attempted_requests"` // xtID hex -> queued request
}
