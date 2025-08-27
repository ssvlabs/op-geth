package events

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	l1contracts "github.com/ssvlabs/rollup-shared-publisher/x/superblock/l1/contracts"
)

// mock sub implements ethereum.Subscription
type mockSub struct{ errCh chan error }

func (m *mockSub) Unsubscribe()      {}
func (m *mockSub) Err() <-chan error { return m.errCh }

// mock client feeds a single log and closes.
type mockLogClient struct {
	hdrTime uint64
}

func (m *mockLogClient) SubscribeFilterLogs(
	ctx context.Context,
	q ethereum.FilterQuery,
	ch chan<- types.Log,
) (ethereum.Subscription, error) {
	// Construct ABI and payload
	binding, _ := l1contracts.NewL2OutputOracleBinding("0x0000000000000000000000000000000000000001")
	a := binding.ABI()
	ev := a.Events["OutputProposed"]

	// Build tuple matching event arg
	type l2BlockArg struct {
		Slot            uint64
		ChainId         []byte
		BlockNumber     uint64
		BlockHash       []byte
		ParentBlockHash []byte
		IncludedXts     [][]byte `abi:"included_xts"`
		Block           []byte
	}
	type superBlockArg struct {
		BlockNumber       uint64       `abi:"blockNumber"`
		Slot              uint64       `abi:"slot"`
		ParentBlockHash   []byte       `abi:"parentBlockHash"`
		MerkleRoot        []byte       `abi:"merkleRoot"`
		Timestamp         *big.Int     `abi:"timestamp"`
		L2Blocks          []l2BlockArg `abi:"l2Blocks"`
		IncludedXTs       [][]byte     `abi:"_includedXTs"`
		TransactionHashes []byte       `abi:"_transactionHashes"`
		Status            uint8        `abi:"status"`
	}
	arg := superBlockArg{
		BlockNumber:     7,
		Slot:            5,
		ParentBlockHash: []byte{1, 2, 3},
		MerkleRoot:      []byte{4, 5, 6},
		Timestamp:       big.NewInt(1730000000),
		L2Blocks: []l2BlockArg{{
			Slot: 5, ChainId: []byte{0xaa}, BlockNumber: 100, BlockHash: []byte{9}, ParentBlockHash: []byte{8},
			IncludedXts: [][]byte{make([]byte, 32)}, Block: []byte{1, 2},
		}},
		IncludedXTs:       [][]byte{make([]byte, 32)},
		TransactionHashes: make([]byte, 32),
		Status:            1,
	}
	data, _ := ev.Inputs.Pack(arg)

	lg := types.Log{
		Address:     binding.Address(),
		Topics:      []common.Hash{ev.ID},
		Data:        data,
		BlockNumber: 12345,
		TxHash:      common.HexToHash("0xdeadbeef"),
		Removed:     false,
	}

	// Emit then close
	go func() {
		ch <- lg
		close(ch)
	}()
	return &mockSub{errCh: make(chan error)}, nil
}

func (m *mockLogClient) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	return &types.Header{Time: m.hdrTime}, nil
}

func TestWatchOutputProposed_EmitsEvent(t *testing.T) {
	t.Parallel()
	binding, _ := l1contracts.NewL2OutputOracleBinding("0x0000000000000000000000000000000000000001")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	client := &mockLogClient{hdrTime: uint64(time.Now().Unix())}
	ch, err := WatchOutputProposed(ctx, client, binding.Address(), binding.ABI())
	if err != nil {
		t.Fatalf("watch err: %v", err)
	}

	ev := <-ch
	if ev == nil {
		t.Fatalf("expected event")
	}
	if ev.SuperblockNumber != 7 {
		t.Fatalf("unexpected number: %d", ev.SuperblockNumber)
	}
	if ev.L1BlockNumber != 12345 {
		t.Fatalf("unexpected l1 block: %d", ev.L1BlockNumber)
	}
	if len(ev.SuperblockHash) == 0 {
		t.Fatalf("expected non-empty hash")
	}
}
