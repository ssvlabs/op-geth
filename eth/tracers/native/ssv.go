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

const RollupAMailBoxAddr = "0x33C061304de440B89BC829bD4dC4eF688E5d1Cef"
const RollupBMailBoxAddr = "0xbB6A1eCF93641122E5c76b6978bb4B7304879Dd5"

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

	t := &SSVTracer{
		operations: make([]ssv.SSVOperation, 0),
		watchedAddresses: map[common.Address]bool{
			common.HexToAddress(RollupAMailBoxAddr): true,
			common.HexToAddress(RollupBMailBoxAddr): true,
		},
	}

	return &tracers.Tracer{
			Hooks: &tracing.Hooks{
				OnEnter:   t.OnEnter,
				OnExit:    t.OnExit,
				OnTxStart: t.OnTxStart,
				OnTxEnd:   t.OnTxEnd,
			},
			GetResult: t.GetResult,
			Stop:      t.Stop,
		},
		nil
}

func (t *SSVTracer) OnTxStart(env *tracing.VMContext, tx *types.Transaction, from common.Address) {
	log.Info("[SSV] OnTxStart called", "txHash", tx.Hash().Hex(), "from", from.Hex())

	t.env = env
}

func (t *SSVTracer) OnTxEnd(_ *types.Receipt, err error) {
	log.Info("[SSV] OnTxEnd called")

	if err != nil {
		return
	}
}

func (t *SSVTracer) OnEnter(depth int, typ byte, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
	log.Info("[SSV] OnEnter called", "depth", depth, "type", vm.OpCode(typ).String(), "from", from.Hex(), "to", to.Hex(), "gas", gas, "value", value)

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
			Gas:      gas,
		}

		log.Info("[SSV] Operation recorded")
		t.operations = append(t.operations, op)
	}
}

func (t *SSVTracer) OnExit(depth int, output []byte, gasUsed uint64, err error, reverted bool) {
	log.Info("[SSV] OnExit called", "depth", depth, "output", common.Bytes2Hex(output), "gasUsed", gasUsed, "err", err, "reverted", reverted)

	if t.interrupt.Load() {
		return
	}

	if depth > 0 {
		t.currentDepth = depth - 1
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
		OnEnter:   t.OnEnter,
		OnTxStart: t.OnTxStart,
		OnTxEnd:   t.OnTxEnd,
		OnExit:    t.OnExit,
	}
}

// GetTraceResult returns the SSVTraceResult containing the operations
func (t *SSVTracer) GetTraceResult() *ssv.SSVTraceResult {
	return &ssv.SSVTraceResult{
		Operations:      t.operations,
		ExecutionResult: nil, // Execution result is not used in this tracer
	}
}
