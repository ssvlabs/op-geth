package superblock

import (
	"context"
	"time"

	pb "github.com/ssvlabs/rollup-shared-publisher/proto/rollup/v1"
)

// SlotManager handles 12-second Ethereum-aligned slots for SBCP coordination
type SlotManager interface {
	GetCurrentSlot() uint64
	GetSlotStartTime(slot uint64) time.Time
	GetSlotProgress() float64
	IsSlotSealTime() bool
	WaitForNextSlot(ctx context.Context) error
	SetGenesisTime(genesis time.Time)
}

// SlotState represents the state of the Shared Publisher during slot execution
type SlotState int

const (
	SlotStateStarting SlotState = iota
	SlotStateFree
	SlotStateLocked
	SlotStateSealing
)

func (s SlotState) String() string {
	switch s {
	case SlotStateStarting:
		return "starting"
	case SlotStateFree:
		return "free"
	case SlotStateLocked:
		return "locked"
	case SlotStateSealing:
		return "sealing"
	default:
		return "unknown"
	}
}

// SuperblockCoordinator manages the overall superblock construction protocol
type SuperblockCoordinator interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	GetCurrentSlot() uint64
	GetSlotState() SlotState
	GetActiveTransactions() []*pb.XtID
	SubmitXTRequest(ctx context.Context, request *pb.XTRequest) error
	GetStats() map[string]interface{}
}
