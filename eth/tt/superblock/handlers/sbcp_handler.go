package handlers

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	pb "github.com/ssvlabs/rollup-shared-publisher/proto/rollup/v1"
	"github.com/ssvlabs/rollup-shared-publisher/x/consensus"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock"
)

// SBCPHandler handles Superblock-specific messages
type SBCPHandler struct {
	coordinator    *superblock.Coordinator
	consensusCoord consensus.Coordinator
	log            zerolog.Logger
}

func NewSBCPHandler(
	coordinator *superblock.Coordinator,
	consensusCoord consensus.Coordinator,
	log zerolog.Logger,
) *SBCPHandler {
	return &SBCPHandler{
		coordinator:    coordinator,
		consensusCoord: consensusCoord,
		log:            log,
	}
}

func (h *SBCPHandler) CanHandle(msg *pb.Message) bool {
	switch msg.Payload.(type) {
	case *pb.Message_StartSlot,
		*pb.Message_RequestSeal,
		*pb.Message_L2Block,
		*pb.Message_CircMessage:
		return true
	default:
		return false
	}
}

func (h *SBCPHandler) Handle(ctx context.Context, from string, msg *pb.Message) error {
	h.log.Debug().
		Str("from", from).
		Str("type", fmt.Sprintf("%T", msg.Payload)).
		Msg("SBCP handler processing")

	switch payload := msg.Payload.(type) {
	case *pb.Message_CircMessage:
		h.log.Debug().
			Str("from", from).
			Str("source", fmt.Sprintf("%x", payload.CircMessage.SourceChain)).
			Str("dest", fmt.Sprintf("%x", payload.CircMessage.DestinationChain)).
			Str("xt_id", payload.CircMessage.XtId.Hex()).
			Msg("CIRC message observed")

		return h.consensusCoord.RecordCIRCMessage(payload.CircMessage)

	case *pb.Message_L2Block:
		return h.coordinator.HandleL2Block(ctx, from, payload.L2Block)

	case *pb.Message_StartSlot:
		// Sequencers don't send this, but handle for completeness
		return nil

	case *pb.Message_RequestSeal:
		// Sequencers don't send this either
		return nil

	case *pb.Message_StartSc:
		// Handle if sequencer echoes back
		return nil

	default:
		return fmt.Errorf("SBCP handler cannot process %T", msg.Payload)
	}
}
