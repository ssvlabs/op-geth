package native

import (
	"encoding/json"
	"github.com/ethereum/go-ethereum/core"
	"math/big"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/params"
)

//go:generate go run github.com/fjl/gencodec -type SSVOperation -field-override ssvOperationMarshaling -out gen_ssv_json.go

func init() {
	tracers.DefaultDirectory.Register("ssvTracer", newSSVTracer, false)
}

// SSVOperation represents a single operation tracked by SSVTracer
type SSVOperation struct {
	Type         vm.OpCode      `json:"-"`       // Operation type (CALL, SLOAD, SSTORE, etc.)
	Address      common.Address `json:"address"` // Contract address
	CallData     []byte         `json:"callData,omitempty" rlp:"optional"`
	StorageKey   []byte         `json:"storageKey,omitempty" rlp:"optional"`
	StorageValue []byte         `json:"storageValue,omitempty" rlp:"optional"`
	From         common.Address `json:"from"` // Caller address
	Gas          uint64         `json:"gas"`  // Gas available
}

func (op SSVOperation) TypeString() string {
	return op.Type.String()
}

// ssvOperationMarshaling provides field overrides for JSON marshaling
type ssvOperationMarshaling struct {
	TypeString   string `json:"type"`
	CallData     hexutil.Bytes
	StorageKey   hexutil.Bytes
	StorageValue hexutil.Bytes
	Gas          hexutil.Uint64
}

// SSVTraceResult contains the simple flat list of operations
type SSVTraceResult struct {
	Operations      []SSVOperation        `json:"operations"`
	ExecutionResult *core.ExecutionResult `json:"-"`
}

// SSVTracer is a minimal tracer for SSV sequencer needs
type SSVTracer struct {
	env              *tracing.VMContext // The VM environment
	operations       []SSVOperation     // Flat list of operations
	interrupt        atomic.Bool        // Atomic flag to signal execution interruption
	reason           error              // Textual reason for the interruption
	watchedAddresses map[common.Address]bool
	currentDepth     int
	currentFrom      common.Address
	currentTo        common.Address
}

// newSSVTracer is the registered constructor.
func newSSVTracer(ctx *tracers.Context, cfg json.RawMessage, chainConfig *params.ChainConfig) (*tracers.Tracer, error) {
	// TODO: The watched addresses should be configurable via cfg
	mockAddr1 := common.HexToAddress("0x1234567890123456789012345678901234567890")
	mockAddr2 := common.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd")

	t := &SSVTracer{
		operations: make([]SSVOperation, 0),
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
			OnTxEnd:         t.OnTxEnd,
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

func (t *SSVTracer) OnTxEnd(_ *types.Receipt, err error) {
	if err != nil {
		return
	}
}

func (t *SSVTracer) OnEnter(depth int, typ byte, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
	if t.interrupt.Load() {
		return
	}

	t.currentDepth = depth
	t.currentFrom = from
	t.currentTo = to

	// Only track calls to watched addresses
	if t.watchedAddresses[to] {
		op := SSVOperation{
			Type:     vm.OpCode(typ),
			Address:  to,
			From:     from,
			CallData: common.CopyBytes(input),
			Gas:      gas,
		}
		t.operations = append(t.operations, op)
	}
}

func (t *SSVTracer) OnExit(depth int, output []byte, gasUsed uint64, err error, reverted bool) {
	if t.interrupt.Load() {
		return
	}
	// Reset current context when exiting
	t.currentDepth = depth - 1
}

func (t *SSVTracer) OnStorageChange(addr common.Address, slot common.Hash, prev, new common.Hash) {
	if t.interrupt.Load() {
		return
	}

	// Only track storage changes for watched addresses
	if !t.watchedAddresses[addr] {
		return
	}

	op := SSVOperation{
		Type:         vm.SSTORE,
		Address:      addr,
		From:         t.currentFrom,
		StorageKey:   slot.Bytes(),
		StorageValue: new.Bytes(),
		Gas:          0, // Gas not available in this context
	}

	t.operations = append(t.operations, op)
}

func (t *SSVTracer) OnOpcode(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	if t.interrupt.Load() {
		return
	}

	opcode := vm.OpCode(op)

	// Track SLOAD operations for watched addresses
	if opcode == vm.SLOAD && len(scope.StackData()) > 0 {
		addr := scope.Address()

		if !t.watchedAddresses[addr] {
			return
		}

		key := scope.StackData()[len(scope.StackData())-1]
		keyHash := common.Hash(key.Bytes32())
		value := t.env.StateDB.GetState(addr, keyHash)

		operation := SSVOperation{
			Type:         vm.SLOAD,
			Address:      addr,
			From:         t.currentFrom,
			StorageKey:   keyHash.Bytes(),
			StorageValue: value.Bytes(),
			Gas:          gas,
		}

		t.operations = append(t.operations, operation)
	}
}

// Stop terminates execution of the tracer at the first opportune moment.
func (t *SSVTracer) Stop(err error) {
	t.reason = err
	t.interrupt.Store(true)
}

// GetResult returns the json-encoded flat list of operations
func (t *SSVTracer) GetResult() (json.RawMessage, error) {
	result := SSVTraceResult{
		Operations: t.operations,
	}

	res, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return res, t.reason
}

/////////// Public API for creating a new SSVTracer instance ///////////

func NewSSVTracer() *SSVTracer {
	// TODO: Replace with actual watched addresses
	mockAddr1 := common.HexToAddress("0x1234567890123456789012345678901234567890")
	mockAddr2 := common.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd")

	return &SSVTracer{
		operations: make([]SSVOperation, 0),
		watchedAddresses: map[common.Address]bool{
			mockAddr1: true,
			mockAddr2: true,
		},
	}
}

func (t *SSVTracer) Hooks() *tracing.Hooks {
	return &tracing.Hooks{
		OnEnter:         t.OnEnter,
		OnTxStart:       t.OnTxStart,
		OnTxEnd:         t.OnTxEnd,
		OnExit:          t.OnExit,
		OnStorageChange: t.OnStorageChange,
		OnOpcode:        t.OnOpcode,
	}
}

// GetTraceResult returns the SSVTraceResult containing the operations
func (t *SSVTracer) GetTraceResult() *SSVTraceResult {
	return &SSVTraceResult{
		Operations:      t.operations,
		ExecutionResult: nil, // Execution result is not used in this tracer
	}
}
