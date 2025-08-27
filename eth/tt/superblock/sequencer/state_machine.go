package sequencer

import (
	"fmt"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

type State int

const (
	StateWaiting State = iota
	StateBuildingFree
	StateBuildingLocked
	StateSubmission
)

func (s State) String() string {
	switch s {
	case StateWaiting:
		return "waiting"
	case StateBuildingFree:
		return "building-free"
	case StateBuildingLocked:
		return "building-locked"
	case StateSubmission:
		return "submission"
	default:
		return "unknown"
	}
}

type StateChangeCallback func(from, to State, slot uint64, reason string)

type StateTransition struct {
	From      State
	To        State
	Slot      uint64
	Timestamp time.Time
	Reason    string
}

type StateMachine struct {
	mu           sync.RWMutex
	currentState State
	currentSlot  uint64
	chainID      []byte
	log          zerolog.Logger
	callback     StateChangeCallback

	// State history
	transitions []StateTransition
}

func NewStateMachine(chainID []byte, log zerolog.Logger, callback StateChangeCallback) *StateMachine {
	return &StateMachine{
		currentState: StateWaiting,
		currentSlot:  0,
		chainID:      chainID,
		log:          log.With().Str("component", "sequencer.state_machine").Logger(),
		callback:     callback,
		transitions:  make([]StateTransition, 0),
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

func (sm *StateMachine) TransitionTo(newState State, slot uint64, reason string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	oldState := sm.currentState

	if !sm.isValidTransition(oldState, newState) {
		return fmt.Errorf("invalid state transition from %s to %s", oldState, newState)
	}

	sm.log.Info().
		Str("from", oldState.String()).
		Str("to", newState.String()).
		Uint64("slot", slot).
		Str("reason", reason).
		Msg("Sequencer state transition")

	sm.currentState = newState
	sm.currentSlot = slot

	// Record transition
	transition := StateTransition{
		From:      oldState,
		To:        newState,
		Slot:      slot,
		Timestamp: time.Now(),
		Reason:    reason,
	}
	sm.transitions = append(sm.transitions, transition)

	// Notify callback
	if sm.callback != nil {
		go sm.callback(oldState, newState, slot, reason)
	}

	return nil
}

func (sm *StateMachine) isValidTransition(from, to State) bool {
	switch from {
	case StateWaiting:
		return to == StateBuildingFree
	case StateBuildingFree:
		return to == StateBuildingLocked || to == StateSubmission || to == StateWaiting
	case StateBuildingLocked:
		return to == StateBuildingFree || to == StateSubmission || to == StateWaiting
	case StateSubmission:
		return to == StateWaiting
	}
	return false
}

func (sm *StateMachine) GetTransitions() []StateTransition {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]StateTransition, len(sm.transitions))
	copy(result, sm.transitions)
	return result
}

func (sm *StateMachine) Reset() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.currentState = StateWaiting
	sm.currentSlot = 0
	sm.transitions = make([]StateTransition, 0)

	sm.log.Info().Msg("State machine reset")
}
