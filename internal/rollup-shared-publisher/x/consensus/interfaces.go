package consensus

import (
	"context"
	"time"

	pb "github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/proto/rollup/v1"
)

// Coordinator defines the consensus coordinator interface
type Coordinator interface {
	// Transaction lifecycle
	StartTransaction(from string, xtReq *pb.XTRequest) error
	RecordVote(xtID *pb.XtID, chainID string, vote bool) (DecisionState, error)
	RecordDecision(xtID *pb.XtID, decision bool) error
	GetTransactionState(xtID *pb.XtID) (DecisionState, error)
	GetActiveTransactions() []*pb.XtID

	// CIRC message handling
	RecordCIRCMessage(circMessage *pb.CIRCMessage) error
	ConsumeCIRCMessage(xtID *pb.XtID, sourceChainID string) (*pb.CIRCMessage, error)

	// Callbacks
	SetStartCallback(fn StartFn)
	SetVoteCallback(fn VoteFn)
	SetDecisionCallback(fn DecisionFn)

	// Lifecycle
	Shutdown() error
}

// Callback function types
type StartFn func(ctx context.Context, from string, xtReq *pb.XTRequest) error
type VoteFn func(ctx context.Context, xtID *pb.XtID, vote bool) error
type DecisionFn func(ctx context.Context, xtID *pb.XtID, decision bool) error

// Config holds coordinator configuration
type Config struct {
	NodeID   string
	IsLeader bool
	Timeout  time.Duration
	Role     Role
}

// DefaultConfig returns sensible defaults
func DefaultConfig(nodeID string) Config {
	return Config{
		NodeID:   nodeID,
		IsLeader: true,
		Timeout:  time.Minute,
		Role:     Leader,
	}
}
