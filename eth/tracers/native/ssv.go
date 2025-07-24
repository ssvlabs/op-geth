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
	mailBoxAddr := common.HexToAddress("0xEd3afBc0af3B010815dd242f1aA20d493Ae3160d")

	t := &SSVTracer{
		operations: make([]ssv.SSVOperation, 0),
		watchedAddresses: map[common.Address]bool{
			mailBoxAddr: true,
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
		},
		nil
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

	// Track calls to watched addresses, including internal calls
	if t.watchedAddresses[to] {
		op := ssv.SSVOperation{
			Type:     vm.OpCode(typ),
			Address:  to,
			From:     from,
			CallData: common.CopyBytes(input),
		}

		log.Debug("[SSV] Operation recorded")
		t.operations = append(t.operations, op)
	}
}

func (t *SSVTracer) OnExit(depth int, output []byte, gasUsed uint64, err error, reverted bool) {
	log.Debug("[SSV] OnExit called")

	if t.interrupt.Load() {
		return
	}

	if depth > 0 {
		t.currentDepth = depth - 1
	}
}

func (t *SSVTracer) OnStorageChange(addr common.Address, slot common.Hash, prev, new common.Hash) {
	log.Debug("[SSV] OnStorageChange called", "address", addr.Hex())

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

	log.Debug("[SSV] Storage operation recorded")

	t.operations = append(t.operations, op)
}

func (t *SSVTracer) OnOpcode(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	if t.interrupt.Load() {
		return
	}

	opcode := vm.OpCode(op)
	addr := scope.Address()

	if !t.watchedAddresses[addr] {
		return
	}

	switch opcode {
	case vm.SLOAD:
		if len(scope.StackData()) > 0 && t.env != nil && t.env.StateDB != nil {
			key := scope.StackData()[len(scope.StackData())-1]
			keyHash := common.Hash(key.Bytes32())
			value := t.env.StateDB.GetState(addr, keyHash)

			operation := ssv.SSVOperation{
				Type:         vm.SLOAD,
				Address:      addr,
				From:         t.currentFrom,
				StorageKey:   keyHash.Bytes(),
				StorageValue: value.Bytes(),
			}

			log.Debug("[SSV] SLOAD operation recorded")
			t.operations = append(t.operations, operation)
		}

	case vm.SSTORE:
		if len(scope.StackData()) >= 2 {
			key := scope.StackData()[len(scope.StackData())-1]
			value := scope.StackData()[len(scope.StackData())-2]

			keyHash := common.Hash(key.Bytes32())
			valueHash := common.Hash(value.Bytes32())

			operation := ssv.SSVOperation{
				Type:         vm.SSTORE,
				Address:      addr,
				From:         t.currentFrom,
				StorageKey:   keyHash.Bytes(),
				StorageValue: valueHash.Bytes(),
			}

			log.Debug("[SSV] SSTORE operation recorded")
			t.operations = append(t.operations, operation)
		}
	}
}

// Stop terminates execution of the tracer at the first opportune moment.
func (t *SSVTracer) Stop(err error) {
	t.reason = err
	t.interrupt.Store(true)
}

// GetResult returns the json-encoded flat list of operations
func (t *SSVTracer) GetResult() (json.RawMessage, error) {
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
