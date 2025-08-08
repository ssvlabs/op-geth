package consensus

import (
	"context"
	"encoding/hex"
	"fmt"
	"github.com/ethereum/go-ethereum/core/types"
	"math/big"
	"sync"
	"time"

	pb "github.com/ethereum/go-ethereum/internal/sp/proto"
	"github.com/ethereum/go-ethereum/log"
)

type Coordinator struct {
	mu      sync.RWMutex
	states  map[string]*TwoPCState
	timeout time.Duration
	metrics *Metrics

	role   Role
	nodeID string

	startCallbackFn    StartFn
	voteCallbackFn     VoteFn
	decisionCallbackFn DecisionFn
	blockCallbackFn    BlockFn

	pendingBlocks   map[string]*types.Block
	pendingBlocksMu sync.RWMutex
}

func NewCoordinator(nodeID string, isLeader bool, timeout time.Duration) *Coordinator {
	role := Follower
	if isLeader {
		role = Leader
	}

	return &Coordinator{
		states:  make(map[string]*TwoPCState),
		timeout: timeout,
		metrics: NewMetrics(),
		role:    role,
		nodeID:  nodeID,
	}
}

func (c *Coordinator) SetDecisionCallback(fn DecisionFn) {
	c.decisionCallbackFn = fn
}

func (c *Coordinator) SetVoteCallback(fn VoteFn) {
	c.voteCallbackFn = fn
}

func (c *Coordinator) SetStartCallback(fn StartFn) {
	c.startCallbackFn = fn
}

func (c *Coordinator) SetBlockCallback(fn BlockFn) {
	c.blockCallbackFn = fn
}

func (c *Coordinator) StartTransaction(from string, xtReq *pb.XTRequest) error {
	xtID, err := xtReq.XtID()
	if err != nil {
		return fmt.Errorf("failed to generate xtID: %w", err)
	}

	chains := xtReq.ChainIDs()
	for chainIDStr := range chains {
		log.Info("[SSV] Participating chain", "chainID", chainIDStr)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	xtIDStr := xtID.Hex()
	if _, exists := c.states[xtIDStr]; exists {
		return fmt.Errorf("transaction %s already exists", xtIDStr)
	}

	state := NewTwoPCState(xtID, xtReq, chains)
	c.states[xtIDStr] = state

	state.Timer = time.AfterFunc(c.timeout, func() {
		c.handleTimeout(xtID)
	})

	c.metrics.RecordTransactionStarted(len(chains))

	log.Info("Started 2PC transaction", "xt_id", xtIDStr, "role", c.role.String(), "participating_chains", len(chains), "timeout", c.timeout)

	if c.startCallbackFn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := c.startCallbackFn(ctx, from, xtReq); err != nil {
			log.Error("Failed to callback start", "error", err, "xt_id", xtIDStr)
		} else {
			//c.metrics.RecordStartCallback(vote)
		}
	}

	return nil
}

func (c *Coordinator) RecordCIRCMessage(circMessage *pb.CIRCMessage) error {
	xtID := circMessage.XtId
	c.mu.Lock()
	xtIDStr := xtID.Hex()
	state, exists := c.states[xtIDStr]
	if !exists {
		c.mu.Unlock()
		return fmt.Errorf("transaction %s not found", xtIDStr)
	}
	c.mu.Unlock()

	state.mu.Lock()
	defer state.mu.Unlock()

	sourceChainID := hex.EncodeToString(circMessage.SourceChain)

	if _, isParticipant := state.ParticipatingChains[sourceChainID]; !isParticipant {
		return fmt.Errorf("chain %s not participating in transaction %s", sourceChainID, xtIDStr)
	}

	sourceChainIDInt := new(big.Int).SetBytes(circMessage.SourceChain)

	log.Info("Received CIRC message", "xt_id", xtIDStr, "role", c.role.String(), "chainID", sourceChainIDInt.String())

	circMessages, ok := state.CIRCMessages[sourceChainID]
	if !ok {
		circMessages = make([]*pb.CIRCMessage, 0)
	}

	circMessages = append(circMessages, circMessage)

	state.CIRCMessages[sourceChainID] = circMessages

	return nil
}

func (c *Coordinator) ConsumeCIRCMessage(xtID *pb.XtID, sourceChainID string) (*pb.CIRCMessage, error) {
	c.mu.Lock()
	xtIDStr := xtID.Hex()
	state, exists := c.states[xtIDStr]
	if !exists {
		c.mu.Unlock()
		return nil, fmt.Errorf("transaction %s not found", xtIDStr)
	}
	c.mu.Unlock()

	state.mu.Lock()
	defer state.mu.Unlock()

	if _, isParticipant := state.ParticipatingChains[sourceChainID]; !isParticipant {
		return nil, fmt.Errorf("chain %s not participating in transaction %s", sourceChainID, xtIDStr)
	}

	circMessages, ok := state.CIRCMessages[sourceChainID]
	if !ok || len(circMessages) == 0 {
		return nil, fmt.Errorf("no messages available for chain %s in transaction %s", sourceChainID, xtIDStr)
	}

	message := circMessages[0]

	state.CIRCMessages[sourceChainID] = circMessages[1:]

	if len(state.CIRCMessages[sourceChainID]) == 0 {
		delete(state.CIRCMessages, sourceChainID)
	}

	return message, nil
}

func (c *Coordinator) RecordVote(xtID *pb.XtID, chainID string, vote bool) (DecisionState, error) {
	c.mu.Lock()
	xtIDStr := xtID.Hex()
	state, exists := c.states[xtIDStr]
	if !exists {
		c.mu.Unlock()
		return StateUndecided, fmt.Errorf("transaction %s not found", xtIDStr)
	}
	c.mu.Unlock()

	state.mu.Lock()
	defer state.mu.Unlock()

	if state.Decision != StateUndecided {
		return state.Decision, nil
	}

	if _, isParticipant := state.ParticipatingChains[chainID]; !isParticipant {
		return StateUndecided, fmt.Errorf("chain %s not participating in transaction %s", chainID, xtIDStr)
	}

	if _, hasVoted := state.Votes[chainID]; hasVoted {
		return StateUndecided, fmt.Errorf("chain %s already voted for transaction %s", chainID, xtIDStr)
	}

	state.Votes[chainID] = vote
	voteLatency := time.Since(state.StartTime)
	c.metrics.RecordVote(chainID, vote, voteLatency)

	log.Info("[SSV] Recorded vote", "xt_id", xtIDStr, "role", c.role.String(), "chain", chainID, "vote", vote, "votes_recorded", len(state.Votes), "votes_required", len(state.ParticipatingChains))

	if !vote {
		return c.handleAbort(xtID, state)
	}

	switch c.role {
	case Leader:
		if len(state.Votes) == len(state.ParticipatingChains) {
			state.Decision = StateCommit
			if state.Timer != nil {
				state.Timer.Stop()
			}
			go c.broadcastDecision(xtID, true, time.Since(state.StartTime))
			return StateCommit, nil
		}
	case Follower:
		go c.broadcastVote(xtID, true, time.Since(state.StartTime))
	}

	return StateUndecided, nil
}

func (c *Coordinator) handleAbort(xtID *pb.XtID, state *TwoPCState) (DecisionState, error) {
	state.Decision = StateAbort
	if state.Timer != nil {
		state.Timer.Stop()
	}

	duration := time.Since(state.StartTime)
	switch c.role {
	case Leader:
		go c.broadcastDecision(xtID, false, duration)
	case Follower:
		go c.broadcastVote(xtID, false, duration)
	}

	return StateAbort, nil
}

func (c *Coordinator) RecordDecision(xtID *pb.XtID, decision bool) error {
	if c.role != Follower {
		return fmt.Errorf("only follower can record decisions, current role: %s", c.role)
	}

	c.mu.Lock()
	xtIDStr := xtID.Hex()
	state, exists := c.states[xtIDStr]
	c.mu.Unlock()

	if !exists {
		log.Debug("Received decision for unknown transaction", "xt_id", xtIDStr, "role", c.role.String(), "decision", decision)
		return nil
	}

	state.mu.Lock()
	defer state.mu.Unlock()

	if state.Decision != StateUndecided {
		log.Debug("Received decision for already committed transaction", "xt_id", xtIDStr, "role", c.role.String(), "decision", decision)
		return nil
	}

	if decision {
		state.Decision = StateCommit
		log.Info("[SSV] Recorded decision", "xt_id", xtIDStr, "role", c.role.String(), "decision", decision, "decision_state", state.Decision.String(), "pending_block", true)
	} else {
		state.Decision = StateAbort
		log.Info("[SSV] Recorded decision", "xt_id", xtIDStr, "role", c.role.String(), "decision", decision, "decision_state", state.Decision.String())
	}

	if state.Timer != nil {
		state.Timer.Stop()
	}

	time.AfterFunc(5*time.Minute, func() {
		c.removeTransaction(xtID)
	})

	return nil
}

func (c *Coordinator) HandleBlockReady(ctx context.Context, block *types.Block) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var committedXTs []*pb.XtID
	var statesToUpdate []*TwoPCState

	// Find committed XTs that haven't been sent to SP yet
	for xtIDStr, state := range c.states {
		state.mu.RLock()
		if state.Decision == StateCommit && !state.BlockSent {
			if xtIDBytes, err := hex.DecodeString(xtIDStr); err == nil {
				committedXTs = append(committedXTs, &pb.XtID{Hash: xtIDBytes})
				statesToUpdate = append(statesToUpdate, state)
			}
		}
		state.mu.RUnlock()
	}

	// Only send if we have new committed XTs
	if len(committedXTs) > 0 && c.blockCallbackFn != nil {
		log.Info("Sending block to SP", "blockHash", block.Hash().Hex(), "committedXTs", len(committedXTs))

		if err := c.blockCallbackFn(ctx, block, committedXTs); err != nil {
			log.Error("Failed to send block to SP", "error", err, "blockHash", block.Hash().Hex())
			return err
		}

		// Mark XTs as sent
		for _, state := range statesToUpdate {
			state.mu.Lock()
			state.BlockSent = true
			state.mu.Unlock()
		}

		log.Info("Block sent to SP successfully", "blockHash", block.Hash().Hex())
	}

	return nil
}

func (c *Coordinator) GetTransactionState(xtID *pb.XtID) (DecisionState, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	xtIDStr := xtID.Hex()
	state, exists := c.states[xtIDStr]
	if !exists {
		return StateUndecided, fmt.Errorf("transaction %s not found", xtIDStr)
	}

	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.Decision, nil
}

func (c *Coordinator) GetActiveTransactions() []*pb.XtID {
	c.mu.RLock()
	defer c.mu.RUnlock()

	ids := make([]*pb.XtID, 0, len(c.states))
	for idStr := range c.states {
		if id, err := hex.DecodeString(idStr); err == nil {
			ids = append(ids, &pb.XtID{Hash: id})
		}
	}
	return ids
}

func (c *Coordinator) handleTimeout(xtID *pb.XtID) {
	c.mu.Lock()
	xtIDStr := xtID.Hex()
	state, exists := c.states[xtIDStr]
	if !exists {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	state.mu.Lock()
	if state.Decision == StateUndecided {
		state.Decision = StateAbort
		state.mu.Unlock()

		log.Warn("Transaction timed out", "xt_id", xtIDStr, "timeout", c.timeout)

		c.metrics.RecordTimeout()
		switch c.role {
		case Leader:
			go c.broadcastDecision(xtID, false, c.timeout)
		case Follower:
			go c.broadcastVote(xtID, false, c.timeout)
		default:
			log.Error("Unsupported role", "role", c.role)
			return
		}
	} else {
		state.mu.Unlock()
	}
}

func (c *Coordinator) broadcastVote(xtID *pb.XtID, vote bool, duration time.Duration) {
	xtIDStr := xtID.Hex()
	log.Info("[SSV] Broadcasting vote", "xt_id", xtIDStr, "vote", vote, "duration", duration)

	if c.voteCallbackFn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := c.voteCallbackFn(ctx, xtID, vote); err != nil {
			log.Error("Failed to broadcast vote", "error", err, "xt_id", xtIDStr, "decision", vote)
		} else {
			c.metrics.RecordVoteBroadcast(vote)
		}
	}

	if !vote {
		time.AfterFunc(5*time.Minute, func() {
			c.removeTransaction(xtID)
		})
	}
}

func (c *Coordinator) broadcastDecision(xtID *pb.XtID, decision bool, duration time.Duration) {
	state := StateCommit
	if !decision {
		state = StateAbort
	}

	c.metrics.RecordTransactionCompleted(state.String(), duration)

	xtIDStr := xtID.Hex()
	log.Info("Broadcasting decision", "xt_id", xtIDStr, "decision", decision, "duration", duration)

	if c.decisionCallbackFn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := c.decisionCallbackFn(ctx, xtID, decision); err != nil {
			log.Error("Failed to broadcast decision", "error", err, "xt_id", xtIDStr, "decision", decision)
		} else {
			c.metrics.RecordDecisionBroadcast(decision)
		}
	}

	time.AfterFunc(5*time.Minute, func() {
		c.removeTransaction(xtID)
	})
}

func (c *Coordinator) removeTransaction(xtID *pb.XtID) {
	c.mu.Lock()
	defer c.mu.Unlock()

	xtIDStr := xtID.Hex()
	if state, exists := c.states[xtIDStr]; exists {
		if state.Timer != nil {
			state.Timer.Stop()
		}
		delete(c.states, xtIDStr)

		log.Debug("Removed transaction state", "xt_id", xtIDStr)
	}
}

// Shutdown gracefully shuts down the coordinator.
func (c *Coordinator) Shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for xtIDStr, state := range c.states {
		if state.Timer != nil {
			state.Timer.Stop()
		}
		log.Debug("Stopping transaction timer during shutdown", "xt_id", xtIDStr)
	}

	log.Info("Coordinator shutdown complete", "active_transactions", len(c.states))
}
