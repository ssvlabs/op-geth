package tracetest

import (
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/tests"
)

// ssvOperation represents a single operation for test output
type ssvOperation struct {
	Type         string         `json:"type"`
	Address      common.Address `json:"address"`
	CallData     hexutil.Bytes  `json:"callData,omitempty"`
	StorageKey   hexutil.Bytes  `json:"storageKey,omitempty"`
	StorageValue hexutil.Bytes  `json:"storageValue,omitempty"`
	From         common.Address `json:"from"`
	Gas          hexutil.Uint64 `json:"gas"`
}

// ssvTraceResult contains the test result structure
type ssvTraceResult struct {
	Operations []ssvOperation `json:"operations"`
}

// ssvTracerTest defines a single test to check the ssv tracer against.
type ssvTracerTest struct {
	tracerTestEnv
	Result *ssvTraceResult `json:"result"`
}

// TestSsvTracer runs the ssv tracer tests.
func TestSsvTracer(t *testing.T) {
	files, err := os.ReadDir(filepath.Join("testdata", "ssv_tracer"))
	if err != nil {
		t.Fatalf("failed to retrieve tracer test suite: %v", err)
	}

	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		t.Run(camel(strings.TrimSuffix(file.Name(), ".json")), func(t *testing.T) {
			t.Parallel()

			var (
				test = new(ssvTracerTest)
				tx   = new(types.Transaction)
			)
			if blob, err := os.ReadFile(filepath.Join("testdata", "ssv_tracer", file.Name())); err != nil {
				t.Fatalf("failed to read testcase: %v", err)
			} else if err := json.Unmarshal(blob, test); err != nil {
				t.Fatalf("failed to parse testcase: %v", err)
			}
			if err := tx.UnmarshalBinary(common.FromHex(test.Input)); err != nil {
				t.Fatalf("failed to parse testcase input: %v", err)
			}

			var (
				signer  = types.MakeSigner(test.Genesis.Config, new(big.Int).SetUint64(uint64(test.Context.Number)), uint64(test.Context.Time))
				st      = tests.MakePreState(rawdb.NewMemoryDatabase(), test.Genesis.Alloc, false, rawdb.HashScheme)
				context = test.Context.toBlockContext(test.Genesis, st.StateDB)
			)
			defer st.Close()

			tracer, err := tracers.DefaultDirectory.New("ssvTracer", new(tracers.Context), test.TracerConfig, test.Genesis.Config)
			if err != nil {
				t.Fatalf("failed to create ssv tracer: %v", err)
			}
			logState := vm.StateDB(st.StateDB)
			if tracer.Hooks != nil {
				logState = state.NewHookedState(st.StateDB, tracer.Hooks)
			}
			msg, err := core.TransactionToMessage(tx, signer, context.BaseFee)
			if err != nil {
				t.Fatalf("failed to prepare transaction for tracing: %v", err)
			}
			evm := vm.NewEVM(context, logState, test.Genesis.Config, vm.Config{Tracer: tracer.Hooks})
			tracer.OnTxStart(evm.GetVMContext(), tx, msg.From)
			vmRet, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(tx.Gas()))
			if err != nil {
				t.Fatalf("failed to execute transaction: %v", err)
			}
			tracer.OnTxEnd(&types.Receipt{GasUsed: vmRet.UsedGas}, nil)

			res, err := tracer.GetResult()
			if err != nil {
				t.Fatalf("failed to retrieve trace result: %v", err)
			}

			// Compare results
			want, err := json.Marshal(test.Result)
			if err != nil {
				t.Fatalf("failed to marshal test result: %v", err)
			}
			if string(want) != string(res) {
				t.Fatalf("trace mismatch\n have: %v\n want: %v\n", string(res), string(want))
			}
		})
	}
}
