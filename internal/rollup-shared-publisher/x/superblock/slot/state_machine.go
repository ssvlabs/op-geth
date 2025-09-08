package slot

import (
	"fmt"
	"sync"
	"time"

	pb "github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/proto/rollup/v1"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/proto"
)

type State int

const (
	StateStarting State = iota
	StateFree
	StateLocked
	StateSealing
)

func (s State) String() string {
	switch s {
	case StateStarting:
		return "starting"
	case StateFree:
		return "free"
	case StateLocked:
		return "locked"
	case StateSealing:
		return "sealing"
	default:
		return "unknown"
	}
}

type StateMachine struct {
	mu           sync.RWMutex
	currentState State
	currentSlot  uint64
	slotManager  *Manager
	log          zerolog.Logger

	nextSuperblockNumber uint64
	lastSuperblockHash   []byte
	activeRollups        [][]byte

	receivedL2Blocks map[string]*pb.L2Block
	scpInstances     map[string]*SCPInstance
	l2BlockRequests  map[string]*pb.L2BlockRequest

	// lastHeads tracks the last known L2 block per chain across slots
	lastHeads map[string]*pb.L2Block

	stateChangeCallbacks map[State][]StateChangeCallback
	transitionHistory    []StateTransition
}

type StateChangeCallback func(from, to State, slot uint64)

type StateTransition struct {
	From      State
	To        State
	Slot      uint64
	Timestamp time.Time
	Reason    string
}

type SCPInstance struct {
	XtID                []byte
	Slot                uint64
	SequenceNumber      uint64
	Request             *pb.XTRequest
	ParticipatingChains [][]byte
	Votes               map[string]bool
	Decision            *bool
	StartTime           time.Time
	DecisionTime        *time.Time
}

func NewStateMachine(slotManager *Manager, log zerolog.Logger) *StateMachine {
	return &StateMachine{
		currentState:         StateStarting,
		currentSlot:          0,
		slotManager:          slotManager,
		log:                  log.With().Str("component", "slot.state_machine").Logger(),
		receivedL2Blocks:     make(map[string]*pb.L2Block),
		scpInstances:         make(map[string]*SCPInstance),
		l2BlockRequests:      make(map[string]*pb.L2BlockRequest),
		lastHeads:            make(map[string]*pb.L2Block),
		stateChangeCallbacks: make(map[State][]StateChangeCallback),
		transitionHistory:    make([]StateTransition, 0),
	}
}

func (sm *StateMachine) GetCurrentState() State {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.currentState
}

func (sm *StateMachine) GetCurrentSlot() uint64 {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.currentSlot
}

// TransitionTo moves SP through SBCP states: Starting→Free→Locked→Sealing
func (sm *StateMachine) TransitionTo(newState State, reason string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	oldState := sm.currentState

	if !sm.isValidTransition(oldState, newState) {
		return fmt.Errorf("invalid state transition from %s to %s", oldState, newState)
	}

	sm.log.Info().
		Str("from", oldState.String()).
		Str("to", newState.String()).
		Uint64("slot", sm.currentSlot).
		Str("reason", reason).
		Msg("State transition")

	sm.currentState = newState

	transition := StateTransition{
		From:      oldState,
		To:        newState,
		Slot:      sm.currentSlot,
		Timestamp: time.Now(),
		Reason:    reason,
	}
	sm.transitionHistory = append(sm.transitionHistory, transition)

	if callbacks, exists := sm.stateChangeCallbacks[newState]; exists {
		for _, callback := range callbacks {
			go callback(oldState, newState, sm.currentSlot)
		}
	}

	return nil
}

func (sm *StateMachine) RegisterStateChangeCallback(state State, callback StateChangeCallback) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.stateChangeCallbacks[state] = append(sm.stateChangeCallbacks[state], callback)
}

func (sm *StateMachine) BeginSlot(slot uint64, superblockNumber uint64, lastHash []byte, activeRollups [][]byte) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.currentSlot = slot
	sm.nextSuperblockNumber = superblockNumber
	sm.lastSuperblockHash = lastHash
	sm.activeRollups = activeRollups

	sm.receivedL2Blocks = make(map[string]*pb.L2Block)
	sm.scpInstances = make(map[string]*SCPInstance)
	sm.l2BlockRequests = make(map[string]*pb.L2BlockRequest)

	for _, chainID := range activeRollups {
		chainIDStr := string(chainID)
		sm.l2BlockRequests[chainIDStr] = &pb.L2BlockRequest{
			ChainId:     chainID,
			BlockNumber: sm.getNextL2BlockNumber(chainID),
			ParentHash:  sm.getHeadL2BlockHash(chainID),
		}
	}

	oldState := sm.currentState
	sm.currentState = StateFree

	sm.log.Info().
		Str("from", oldState.String()).
		Str("to", sm.currentState.String()).
		Uint64("slot", sm.currentSlot).
		Uint64("superblock_number", sm.nextSuperblockNumber).
		Int("active_rollups_count", len(sm.activeRollups)).
		Msg("Beginning new slot")

	return nil
}

func (sm *StateMachine) StartSCP(instance *SCPInstance) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.currentState != StateFree {
		return fmt.Errorf("cannot start SCP in state %s", sm.currentState)
	}

	instanceID := string(instance.XtID)
	sm.scpInstances[instanceID] = instance

	oldState := sm.currentState
	sm.currentState = StateLocked

	sm.log.Info().
		Str("from", oldState.String()).
		Str("to", sm.currentState.String()).
		Uint64("slot", sm.currentSlot).
		Str("xt_id", fmt.Sprintf("%x", instance.XtID)).
		Msg("Started SCP instance, state locked")

	return nil
}

func (sm *StateMachine) ProcessSCPDecision(xtID []byte, decision bool) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	instance, exists := sm.scpInstances[string(xtID)]
	if !exists {
		return fmt.Errorf("SCP instance not found for xt_id %x", xtID)
	}

	instance.Decision = &decision
	now := time.Now()
	instance.DecisionTime = &now

	sm.log.Info().
		Str("xt_id", fmt.Sprintf("%x", xtID)).
		Bool("decision", decision).
		Uint64("slot", sm.currentSlot).
		Msg("SCP decision processed")

	if sm.currentState == StateLocked {
		oldState := sm.currentState
		sm.currentState = StateFree
		sm.log.Info().
			Str("from", oldState.String()).
			Str("to", sm.currentState.String()).
			Uint64("slot", sm.currentSlot).
			Msg("State unlocked after SCP decision")
	}

	return nil
}

func (sm *StateMachine) RequestSeal(includedXTs [][]byte) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.currentState != StateFree && sm.currentState != StateLocked {
		return fmt.Errorf("cannot request seal in state %s", sm.currentState)
	}

	oldState := sm.currentState
	sm.currentState = StateSealing

	sm.log.Info().
		Str("from", oldState.String()).
		Str("to", sm.currentState.String()).
		Uint64("slot", sm.currentSlot).
		Int("included_xts_count", len(includedXTs)).
		Msg("Sealing slot")

	return nil
}

func (sm *StateMachine) ReceiveL2Block(block *pb.L2Block) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.currentState != StateSealing {
		return fmt.Errorf("not accepting L2 blocks in state %s", sm.currentState)
	}

	chainIDStr := string(block.ChainId)
	sm.receivedL2Blocks[chainIDStr] = block

	// Persist as last head so the next slot’s StartSlot carries a correct
	// parent hash and next block number for this chain.
	if prev, ok := sm.lastHeads[chainIDStr]; ok {
		if block.BlockNumber >= prev.BlockNumber {
			sm.lastHeads[chainIDStr] = block
		}
	} else {
		sm.lastHeads[chainIDStr] = block
	}

	sm.log.Info().
		Str("chain_id", fmt.Sprintf("%x", block.ChainId)).
		Uint64("block_number", block.BlockNumber).
		Uint64("slot", sm.currentSlot).
		Msg("Received L2 block")

	return nil
}

func (sm *StateMachine) GetReceivedL2Blocks() map[string]*pb.L2Block {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make(map[string]*pb.L2Block)
	for k, v := range sm.receivedL2Blocks {
		result[k] = v
	}
	return result
}

func (sm *StateMachine) GetSCPInstances() map[string]*SCPInstance {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make(map[string]*SCPInstance)
	for k, v := range sm.scpInstances {
		result[k] = v
	}
	return result
}

func (sm *StateMachine) GetL2BlockRequests() map[string]*pb.L2BlockRequest {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make(map[string]*pb.L2BlockRequest)
	for k, v := range sm.l2BlockRequests {
		result[k] = v
	}
	return result
}

func (sm *StateMachine) GetActiveRollups() [][]byte {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([][]byte, len(sm.activeRollups))
	copy(result, sm.activeRollups)
	return result
}

func (sm *StateMachine) CheckAllL2BlocksReceived() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for _, chainID := range sm.activeRollups {
		if _, received := sm.receivedL2Blocks[string(chainID)]; !received {
			return false
		}
	}
	return true
}

func (sm *StateMachine) GetIncludedXTs() [][]byte {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var includedXTs [][]byte
	for _, instance := range sm.scpInstances {
		if instance.Decision != nil && *instance.Decision {
			includedXTs = append(includedXTs, instance.XtID)
		}
	}
	return includedXTs
}

func (sm *StateMachine) Reset() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.currentState = StateStarting
	sm.currentSlot = 0
	sm.receivedL2Blocks = make(map[string]*pb.L2Block)
	sm.scpInstances = make(map[string]*SCPInstance)
	sm.l2BlockRequests = make(map[string]*pb.L2BlockRequest)
	// Do not clear lastHeads; we need continuity across slots.
}

func (sm *StateMachine) GetTransitionHistory() []StateTransition {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]StateTransition, len(sm.transitionHistory))
	copy(result, sm.transitionHistory)
	return result
}

// SeedLastHead seeds the last known L2 head for a chain. This allows BeginSlot
// to compute correct L2BlockRequest values (next block number and parent hash)
// even on the first slot or after restarts.
func (sm *StateMachine) SeedLastHead(block *pb.L2Block) {
	if block == nil || len(block.ChainId) == 0 {
		return
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	chainIDStr := string(block.ChainId)
	if prev, ok := sm.lastHeads[chainIDStr]; !ok || prev == nil || block.BlockNumber >= prev.BlockNumber {
		cp := proto.Clone(block).(*pb.L2Block)
		sm.lastHeads[chainIDStr] = cp
	}
}

func (sm *StateMachine) isValidTransition(from, to State) bool {
	switch from {
	case StateStarting:
		return to == StateFree
	case StateFree:
		return to == StateLocked || to == StateSealing
	case StateLocked:
		return to == StateFree || to == StateSealing
	case StateSealing:
		return to == StateStarting
	}
	return false
}

func (sm *StateMachine) getNextL2BlockNumber(chainID []byte) uint64 {
	if head, ok := sm.lastHeads[string(chainID)]; ok && head != nil {
		if head.BlockNumber > 0 {
			return head.BlockNumber + 1
		}
	}
	// TODO: Unknown head: use 0 as sentinel ("unspecified") to allow tolerant validation
	return 0
}

func (sm *StateMachine) getHeadL2BlockHash(chainID []byte) []byte {
	if head, ok := sm.lastHeads[string(chainID)]; ok && head != nil {
		if len(head.BlockHash) == 32 {
			out := make([]byte, 32)
			copy(out, head.BlockHash)
			return out
		}
	}
	// TODO: Unknown parent: return nil to signal "no constraint" to validators
	return nil
}
