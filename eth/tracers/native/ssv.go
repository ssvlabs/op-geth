package native

import (
	"encoding/json"
	"github.com/ethereum/go-ethereum/core/ssv"
	"math/big"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

func init() {
	tracers.DefaultDirectory.Register("ssvTracer", newSSVTracer, false)
}

// SSVTracer is a minimal tracer for SSV sequencer needs
type SSVTracer struct {
	env              *tracing.VMContext // The VM environment
	operations       []ssv.SSVOperation // Flat list of operations
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
		operations: make([]ssv.SSVOperation, 0),
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
	log.Debug("[SSV] OnTxStart called", "txHash", tx.Hash().Hex(), "from", from.Hex())
	t.env = env
}

func (t *SSVTracer) OnTxEnd(_ *types.Receipt, err error) {
	log.Debug("[SSV] OnTxEnd called")

	if err != nil {
		return
	}
}

func (t *SSVTracer) OnEnter(depth int, typ byte, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
	log.Debug("[SSV] OnEnter called")
	if t.interrupt.Load() {
		return
	}

	t.currentDepth = depth
	t.currentFrom = from
	t.currentTo = to

	// Only track calls to watched addresses
	if t.watchedAddresses[to] {
		op := ssv.SSVOperation{
			Type:     vm.OpCode(typ),
			Address:  to,
			From:     from,
			CallData: common.CopyBytes(input),
			Gas:      gas,
		}
		log.Debug("[SSV] Operation recorded", "type", op.Type, "address", to.Hex(), "from", from.Hex(), "gas", gas)
		t.operations = append(t.operations, op)
	}
}

func (t *SSVTracer) OnExit(depth int, output []byte, gasUsed uint64, err error, reverted bool) {
	log.Debug("[SSV] OnExit called")
	if t.interrupt.Load() {
		return
	}
	// Reset current context when exiting
	t.currentDepth = depth - 1
}

func (t *SSVTracer) OnStorageChange(addr common.Address, slot common.Hash, prev, new common.Hash) {
	log.Debug("[SSV] OnStorageChange called", "address", addr.Hex(), "slot", slot.Hex(), "prev", prev.Hex(), "new", new.Hex())
	if t.interrupt.Load() {
		return
	}

	// Only track storage changes for watched addresses
	if !t.watchedAddresses[addr] {
		return
	}

	op := ssv.SSVOperation{
		Type:         vm.SSTORE,
		Address:      addr,
		From:         t.currentFrom,
		StorageKey:   slot.Bytes(),
		StorageValue: new.Bytes(),
	}

	log.Debug("[SSV] Storage operation recorded", "type", op.Type, "address", addr.Hex(), "from", t.currentFrom.Hex(), "storageKey", slot.Hex(), "storageValue", new.Hex())

	t.operations = append(t.operations, op)
}

func (t *SSVTracer) OnOpcode(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	log.Debug("[SSV] OnOpcode called")
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

		operation := ssv.SSVOperation{
			Type:         vm.SLOAD,
			Address:      addr,
			From:         t.currentFrom,
			StorageKey:   keyHash.Bytes(),
			StorageValue: value.Bytes(),
			Gas:          gas,
		}

		log.Debug("[SSV] SLOAD operation recorded", "address", addr.Hex(), "storageKey", keyHash.Hex(), "storageValue", value.Hex(), "gas", gas)

		t.operations = append(t.operations, operation)
	}
}

// Stop terminates execution of the tracer at the first opportune moment.
func (t *SSVTracer) Stop(err error) {
	log.Warn("[SSV] Tracer stopped", "reason", err)

	t.reason = err
	t.interrupt.Store(true)
}

// GetResult returns the json-encoded flat list of operations
func (t *SSVTracer) GetResult() (json.RawMessage, error) {
	log.Debug("[SSV] GetResult called")
	result := ssv.SSVTraceResult{
		Operations: t.operations,
	}

	res, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return res, t.reason
}

/////////// Public API for creating a new SSVTracer instance ///////////

func NewSSVTracer(mailboxAddresses []common.Address) *SSVTracer {
	watchedAddresses := make(map[common.Address]bool)
	for _, addr := range mailboxAddresses {
		watchedAddresses[addr] = true
	}

	log.Info("[SSV] Creating new SSVTracer with watched addresses", "addresses", watchedAddresses)

	return &SSVTracer{
		operations:       make([]ssv.SSVOperation, 0),
		watchedAddresses: watchedAddresses,
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
func (t *SSVTracer) GetTraceResult() *ssv.SSVTraceResult {
	return &ssv.SSVTraceResult{
		Operations:      t.operations,
		ExecutionResult: nil, // Execution result is not used in this tracer
	}
}
