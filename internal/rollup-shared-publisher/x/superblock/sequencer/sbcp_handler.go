package sequencer

import (
	"context"
	"fmt"

	pb "github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/proto/rollup/v1"
	"github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/x/superblock/protocol"
	"github.com/rs/zerolog"
)

// sbcpHandler implements the protocol.MessageHandler interface for SBCP messages
type sbcpHandler struct {
	coordinator *SequencerCoordinator
	log         zerolog.Logger
}

// NewSBCPHandler creates a new SBCP message handler
func NewSBCPHandler(coordinator *SequencerCoordinator, log zerolog.Logger) protocol.MessageHandler {
	return &sbcpHandler{
		coordinator: coordinator,
		log:         log.With().Str("component", "sbcp_handler").Logger(),
	}
}

// HandleStartSlot processes StartSlot messages
func (h *sbcpHandler) HandleStartSlot(ctx context.Context, from string, startSlot *pb.StartSlot) error {
	h.log.Info().
		Str("from", from).
		Uint64("slot", startSlot.Slot).
		Uint64("superblock_number", startSlot.NextSuperblockNumber).
		Int("l2_requests", len(startSlot.L2BlocksRequest)).
		Msg("Processing StartSlot message")

	// Delegate to coordinator's existing logic
	return h.coordinator.handleStartSlot(startSlot)
}

// HandleRequestSeal processes RequestSeal messages
func (h *sbcpHandler) HandleRequestSeal(ctx context.Context, from string, requestSeal *pb.RequestSeal) error {
	h.log.Info().
		Str("from", from).
		Uint64("slot", requestSeal.Slot).
		Int("included_xts", len(requestSeal.IncludedXts)).
		Msg("Processing RequestSeal message")

	// Delegate to coordinator's existing logic
	return h.coordinator.handleRequestSeal(ctx, from, requestSeal)
}

// HandleL2Block processes L2Block messages (typically not received by sequencers)
func (h *sbcpHandler) HandleL2Block(ctx context.Context, from string, l2Block *pb.L2Block) error {
	h.log.Warn().
		Str("from", from).
		Uint64("slot", l2Block.Slot).
		Uint64("block_number", l2Block.BlockNumber).
		Msg("Received unexpected L2Block message - sequencers typically don't receive these")

	// Sequencers don't usually receive L2Block messages, but we'll log it
	return fmt.Errorf("sequencer received unexpected L2Block from %s", from)
}

// HandleStartSC processes StartSC messages (cross-chain transaction initiation)
func (h *sbcpHandler) HandleStartSC(ctx context.Context, from string, startSC *pb.StartSC) error {
	h.log.Info().
		Str("from", from).
		Uint64("slot", startSC.Slot).
		Uint64("sequence", startSC.XtSequenceNumber).
		Msg("Processing StartSC message")

	// Delegate to coordinator's existing logic
	return h.coordinator.handleStartSC(ctx, from, startSC)
}

// HandleRollBackAndStartSlot processes rollback messages
func (h *sbcpHandler) HandleRollBackAndStartSlot(ctx context.Context, from string, rb *pb.RollBackAndStartSlot) error {
	h.log.Info().
		Str("from", from).
		Uint64("current_slot", rb.CurrentSlot).
		Uint64("superblock_number", rb.NextSuperblockNumber).
		Int("l2_requests", len(rb.L2BlocksRequest)).
		Msg("Processing RollBackAndStartSlot message")

	return h.coordinator.handleRollBackAndStartSlot(from, rb)
}
