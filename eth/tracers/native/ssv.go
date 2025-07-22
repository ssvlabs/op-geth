package native

import (
	"encoding/json"
	"math/big"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/params"
)

func init() {
	tracers.DefaultDirectory.Register("ssvTracer", newSSVTracer, false)
}

// SSVOperation represents a single operation tracked by SSVTracer
type SSVOperation struct {
	OpCode       vm.OpCode      `json:"opcode"`
	Address      common.Address `json:"address"`
	CallData     []byte         `json:"callData,omitempty"`
	StorageKey   []byte         `json:"storageKey,omitempty"`
	StorageValue []byte         `json:"storageValue,omitempty"`
	From         common.Address `json:"from,omitempty"`
	Gas          uint64         `json:"gas,omitempty"`
}

// SSVCall represents a call in the trace
type SSVCall struct {
	Depth      int            `json:"depth"`
	From       common.Address `json:"from"`
	To         common.Address `json:"to"`
	Operations []SSVOperation `json:"operations"`
	Subcalls   []*SSVCall     `json:"subcalls,omitempty"`
}

// SSVTraceResult contains the trace results
type SSVTraceResult struct {
	RootCall        *SSVCall              `json:"rootCall"`
	ExecutionResult *core.ExecutionResult `json:"-"`
}

// SSVTracer is a minimal tracer for SSV sequencer needs
type SSVTracer struct {
	env *tracing.VMContext // The env environment

	callStack        []*SSVCall
	currentCall      *SSVCall
	rootCall         *SSVCall
	interrupt        atomic.Bool
	err              error
	watchedAddresses map[common.Address]bool
}

// newSSVTracer is the registered constructor.
func newSSVTracer(ctx *tracers.Context, cfg json.RawMessage, chainConfig *params.ChainConfig) (*tracers.Tracer, error) {
	// TODO: The watched addresses are currently mocked. In a real scenario,
	// these would likely be passed in via the `cfg` json.RawMessage.
	mockAddr1 := common.HexToAddress("0x1234567890123456789012345678901234567890")
	mockAddr2 := common.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd")

	t := &SSVTracer{
		callStack: make([]*SSVCall, 0),
		watchedAddresses: map[common.Address]bool{
			mockAddr1: true,
			mockAddr2: true,
		},
	}

	return &tracers.Tracer{
		Hooks: &tracing.Hooks{
			OnEnter:         t.OnEnter,
			OnExit:          t.OnExit,
			OnTxStart:       t.OnTxStart,
			OnStorageChange: t.OnStorageChange,
			OnOpcode:        t.OnOpcode,
		},
		GetResult: t.GetResult,
		Stop:      t.Stop,
	}, nil
}

func (t *SSVTracer) OnTxStart(env *tracing.VMContext, tx *types.Transaction, from common.Address) {
	t.env = env
}

func (t *SSVTracer) OnEnter(depth int, typ byte, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
	if t.interrupt.Load() {
		return
	}

	call := &SSVCall{
		Depth:      depth,
		From:       from,
		To:         to,
		Operations: make([]SSVOperation, 0),
		Subcalls:   make([]*SSVCall, 0),
	}

	if t.watchedAddresses[to] {
		op := SSVOperation{
			OpCode:   vm.OpCode(typ),
			Address:  to,
			From:     from,
			CallData: common.CopyBytes(input),
			Gas:      gas,
		}
		call.Operations = append(call.Operations, op)
	}

	if depth == 0 {
		t.rootCall = call
		t.currentCall = call
	} else {
		if t.currentCall != nil {
			t.currentCall.Subcalls = append(t.currentCall.Subcalls, call)
		}
		t.currentCall = call
	}

	t.callStack = append(t.callStack, call)
}

func (t *SSVTracer) OnExit(depth int, output []byte, gasUsed uint64, err error, reverted bool) {
	if t.interrupt.Load() || len(t.callStack) == 0 {
		return
	}

	if len(t.callStack) > 1 {
		t.callStack = t.callStack[:len(t.callStack)-1]
		t.currentCall = t.callStack[len(t.callStack)-1]
	} else {
		t.currentCall = nil
	}
}

func (t *SSVTracer) OnStorageChange(addr common.Address, slot common.Hash, prev, new common.Hash) {
	if t.interrupt.Load() || t.currentCall == nil {
		return
	}

	if !t.watchedAddresses[addr] {
		return
	}

	op := SSVOperation{
		OpCode:       vm.SSTORE,
		Address:      addr,
		StorageKey:   slot.Bytes(),
		StorageValue: new.Bytes(),
	}

	t.currentCall.Operations = append(t.currentCall.Operations, op)
}

func (t *SSVTracer) OnOpcode(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	if t.interrupt.Load() || t.currentCall == nil {
		return
	}

	opcode := vm.OpCode(op)

	if opcode == vm.SLOAD && len(scope.StackData()) > 0 {
		addr := scope.Address()

		if !t.watchedAddresses[addr] {
			return
		}

		key := scope.StackData()[len(scope.StackData())-1]
		keyHash := common.Hash(key.Bytes32())
		value := t.env.StateDB.GetState(addr, keyHash)

		operation := SSVOperation{
			OpCode:       opcode,
			Address:      addr,
			StorageKey:   common.Hash(key.Bytes32()).Bytes(),
			StorageValue: value.Bytes(),
		}

		t.currentCall.Operations = append(t.currentCall.Operations, operation)
	}

}

func (t *SSVTracer) Stop(err error) {
	t.err = err
	t.interrupt.Store(true)
}

func (t *SSVTracer) GetResult() (json.RawMessage, error) {
	result := &SSVTraceResult{
		RootCall: t.rootCall,
	}
	res, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return res, t.err
}

func (t *SSVTracer) GetFlatOperations() []SSVOperation {
	if t.rootCall == nil {
		return nil
	}

	var ops []SSVOperation
	var extractOps func(*SSVCall)

	extractOps = func(call *SSVCall) {
		ops = append(ops, call.Operations...)
		for _, subcall := range call.Subcalls {
			extractOps(subcall)
		}
	}

	extractOps(t.rootCall)
	return ops
}