package l1

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/rs/zerolog"
	pb "github.com/ssvlabs/rollup-shared-publisher/proto/rollup/v1"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/l1/contracts"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/store"
)

type mockEthClient struct {
	sent *types.Transaction
}

func (m *mockEthClient) ChainID(ctx context.Context) (*big.Int, error) { return big.NewInt(1337), nil }
func (m *mockEthClient) PendingNonceAt(ctx context.Context, account common.Address) (uint64, error) {
	return 7, nil
}
func (m *mockEthClient) SuggestGasTipCap(ctx context.Context) (*big.Int, error) {
	return big.NewInt(2_000_000_000), nil
}
func (m *mockEthClient) SuggestGasPrice(ctx context.Context) (*big.Int, error) {
	return big.NewInt(3_000_000_000), nil
}
func (m *mockEthClient) EstimateGas(ctx context.Context, msg ethereum.CallMsg) (uint64, error) {
	return 100_000, nil
}
func (m *mockEthClient) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	return &types.Header{
		Number:  big.NewInt(100),
		BaseFee: big.NewInt(10_000_000_000),
		Time:    uint64(time.Now().Unix()),
	}, nil
}
func (m *mockEthClient) SendTransaction(ctx context.Context, tx *types.Transaction) error {
	m.sent = tx
	return nil
}
func (m *mockEthClient) TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error) {
	return nil, ethereum.NotFound
}

func (m *mockEthClient) SubscribeFilterLogs(
	ctx context.Context,
	q ethereum.FilterQuery,
	ch chan<- types.Log,
) (ethereum.Subscription, error) {
	return nil, nil
}

type mockBinding struct{ addr common.Address }

func (b *mockBinding) Address() common.Address { return b.addr }
func (b *mockBinding) BuildPublishCalldata(ctx context.Context, sb *store.Superblock) ([]byte, error) {
	return []byte{0xde, 0xad, 0xbe, 0xef}, nil
}

var _ contracts.Binding = (*mockBinding)(nil)

func TestPublishSuperblock_SignsAndSends(t *testing.T) {
	ctx := context.Background()
	key, _ := crypto.GenerateKey()
	signer := NewLocalECDSASigner(big.NewInt(1337), key)

	client := &mockEthClient{}
	binding := &mockBinding{addr: common.HexToAddress("0x000000000000000000000000000000000000dead")}
	cfg := DefaultConfig()
	cfg.ChainID = 1337
	cfg.GasLimitBufferPct = 0

	pub := &EthPublisher{cfg: cfg, client: client, signer: signer, contract: binding, log: zerolog.Nop()}

	sb := &store.Superblock{
		Number:     1,
		Slot:       1,
		ParentHash: make([]byte, 32),
		Timestamp:  time.Now(),
		L2Blocks:   []*pb.L2Block{},
	}
	tx, err := pub.PublishSuperblock(ctx, sb)
	if err != nil {
		t.Fatalf("PublishSuperblock error: %v", err)
	}
	if tx == nil || len(tx.Hash) == 0 {
		t.Fatalf("expected tx hash")
	}
	if client.sent == nil {
		t.Fatalf("expected transaction to be sent")
	}
	if *client.sent.To() != binding.addr {
		t.Fatalf("unexpected tx to: %s", client.sent.To().Hex())
	}
	if have := client.sent.Data(); len(have) != 4 || have[0] != 0xde {
		t.Fatalf("unexpected calldata")
	}
	if client.sent.Nonce() != 7 {
		t.Fatalf("unexpected nonce: %d", client.sent.Nonce())
	}
}
