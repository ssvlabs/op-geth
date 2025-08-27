package protocol

import (
	"context"

	pb "github.com/ssvlabs/rollup-shared-publisher/proto/rollup/v1"
)

// Handler defines the interface for SBCP protocol message handling
type Handler interface {
	// Handle processes SBCP protocol messages
	Handle(ctx context.Context, from string, msg *pb.Message) error

	// CanHandle returns true if this handler can process the message
	CanHandle(msg *pb.Message) bool

	// GetProtocolName returns the protocol name for logging/debugging
	GetProtocolName() string
}

// MessageHandler defines handlers for specific SBCP message types
type MessageHandler interface {
	HandleStartSlot(ctx context.Context, from string, startSlot *pb.StartSlot) error
	HandleRequestSeal(ctx context.Context, from string, requestSeal *pb.RequestSeal) error
	HandleL2Block(ctx context.Context, from string, l2Block *pb.L2Block) error
	HandleStartSC(ctx context.Context, from string, startSC *pb.StartSC) error
	HandleRollBackAndStartSlot(ctx context.Context, from string, rb *pb.RollBackAndStartSlot) error
}

// Validator defines message validation interface
type Validator interface {
	ValidateStartSlot(startSlot *pb.StartSlot) error
	ValidateRequestSeal(requestSeal *pb.RequestSeal) error
	ValidateL2Block(l2Block *pb.L2Block) error
	ValidateStartSC(startSC *pb.StartSC) error
	ValidateRollBackAndStartSlot(rb *pb.RollBackAndStartSlot) error
}
