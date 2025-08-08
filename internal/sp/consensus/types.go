package consensus

import (
	"context"
	"github.com/ethereum/go-ethereum/core/types"
	rollupv1 "github.com/ssvlabs/rollup-shared-publisher/proto/rollup/v1"

	"sync"
	"time"
)

type StartFn func(ctx context.Context, from string, xtReq *rollupv1.XTRequest) error
type VoteFn func(ctx context.Context, xtID *rollupv1.XtID, vote bool) error
type DecisionFn func(ctx context.Context, xtID *rollupv1.XtID, decision bool) error

type BlockFn func(ctx context.Context, block *types.Block, xtIDs []*rollupv1.XtID) error

// Role represents the role of a coordinator in the consensus system
type Role int

const (
	Follower Role = iota
	Leader
)

func (r Role) String() string {
	switch r {
	case Follower:
		return "follower"
	case Leader:
		return "leader"
	default:
		return "unknown"
	}
}

// DecisionState represents the possible outcomes of the 2PC protocol.
type DecisionState int

const (
	StateUndecided DecisionState = iota
	StateCommit
	StateAbort
)

func (s DecisionState) String() string {
	switch s {
	case StateUndecided:
		return "undecided"
	case StateCommit:
		return "commit"
	case StateAbort:
		return "abort"
	default:
		return "unknown"
	}
}

// TwoPCState holds the state for a single cross-chain transaction.
type TwoPCState struct {
	mu                  sync.RWMutex
	XTID                *rollupv1.XtID
	Decision            DecisionState
	ParticipatingChains map[string]struct{}
	Votes               map[string]bool
	Timer               *time.Timer
	StartTime           time.Time
	XTRequest           *rollupv1.XTRequest
	CIRCMessages        map[string][]*rollupv1.CIRCMessage

	BlockSent bool
}

// NewTwoPCState creates a new 2PC state.
func NewTwoPCState(xtID *rollupv1.XtID, req *rollupv1.XTRequest, chains map[string]struct{}) *TwoPCState {
	return &TwoPCState{
		XTID:                xtID,
		Decision:            StateUndecided,
		ParticipatingChains: chains,
		Votes:               make(map[string]bool),
		StartTime:           time.Now(),
		XTRequest:           req,
		CIRCMessages:        make(map[string][]*rollupv1.CIRCMessage, 0),
	}
}

// GetVoteCount returns the number of votes received.
func (t *TwoPCState) GetVoteCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.Votes)
}

// GetParticipantCount returns the number of participating chains.
func (t *TwoPCState) GetParticipantCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.ParticipatingChains)
}

// IsComplete returns true if the transaction has reached a final decision.
func (t *TwoPCState) IsComplete() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Decision != StateUndecided
}

// GetDuration returns the duration since the transaction started.
func (t *TwoPCState) GetDuration() time.Duration {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return time.Since(t.StartTime)
}
