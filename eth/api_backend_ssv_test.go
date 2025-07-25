// Copyright 2025 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package eth

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/beacon"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/stretchr/testify/require"
)

var (
	// SSV test key and address
	ssvKey, _  = crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	ssvAddr    = crypto.PubkeyToAddress(ssvKey.PublicKey)
	ssvBalance = new(big.Int).Mul(big.NewInt(1e6), big.NewInt(params.Ether))

	// Contract addresses
	storageAddr = common.HexToAddress("0x1000000000000000000000000000000000000001")
	mailboxA    = common.HexToAddress("0x3000000000000000000000000000000000000003")
	mailboxB    = common.HexToAddress("0x4000000000000000000000000000000000000004")

	// Simple storage contract runtime bytecode
	// contract Storage {
	//     uint256 public data;
	//     function write(uint256 value) public { data = value; }
	//     function read() public view returns (uint256) { return data; }
	// }
	storageCode = common.FromHex("608060405234801561001057600080fd5b50600436106100365760003560e01c80632e64cec11461003b5780636057361d14610059575b600080fd5b610043610075565b60405161005091906100d9565b60405180910390f35b610073600480360381019061006e919061009d565b61007e565b005b60008054905090565b8060008190555050565b60008135905061009781610103565b92915050565b6000602082840312156100b3576100b26100fe565b5b60006100c184828501610088565b91505092915050565b6100d3816100f4565b82525050565b60006020820190506100ee60008301846100ca565b92915050565b6000819050919050565b600080fd5b61010c816100f4565b811461011757600080fd5b5056fea2646970667358221220d4f4525e2615b19e77ec3aa1748a507b878b702f4e4e7a4ba85a0f8a5a77b47464736f6c63430008130033")
)

func initSSVBackendWithContracts(t *testing.T, mailboxAddresses []common.Address, contractSetup map[common.Address][]byte) (*EthAPIBackend, *core.BlockChain) {
	var (
		db     = rawdb.NewMemoryDatabase()
		engine = beacon.New(ethash.NewFaker())
	)

	// Build genesis alloc with test account and contracts
	alloc := types.GenesisAlloc{
		ssvAddr: {Balance: ssvBalance},
	}

	// Add contracts to genesis
	for addr, code := range contractSetup {
		alloc[addr] = types.Account{
			Balance: big.NewInt(0),
			Code:    code,
		}
	}

	gspec := &core.Genesis{
		Config:     params.TestChainConfig,
		Difficulty: common.Big0,
		Alloc:      alloc,
		GasLimit:   10000000,
		BaseFee:    big.NewInt(params.InitialBaseFee),
	}

	chain, err := core.NewBlockChain(db, nil, gspec, nil, engine, vm.Config{}, nil)
	require.NoError(t, err)

	eth := &Ethereum{
		blockchain: chain,
		chainDb:    db,
	}

	return &EthAPIBackend{eth: eth}, chain
}

func TestSimulateTransactionWithSSVTrace_NoMailboxInteraction(t *testing.T) {
	contracts := map[common.Address][]byte{
		storageAddr: storageCode,
	}
	backend, _ := initSSVBackendWithContracts(t, []common.Address{}, contracts)

	// Create transaction data for write(42)
	// Function selector for write(uint256): 0x6057361d
	data := common.FromHex("6057361d000000000000000000000000000000000000000000000000000000000000002a")

	tx := types.NewTransaction(0, storageAddr, big.NewInt(0), 100000, big.NewInt(params.GWei), data)
	signedTx, err := types.SignTx(tx, types.LatestSignerForChainID(backend.ChainConfig().ChainID), ssvKey)
	require.NoError(t, err)

	ctx := t.Context()
	result, err := backend.SimulateTransactionWithSSVTrace(ctx, signedTx, rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber))
	require.NoError(t, err)

	require.Equal(t, 0, len(result.Operations))
	require.False(t, result.ExecutionResult.Failed())
	require.Greater(t, result.ExecutionResult.UsedGas, uint64(0))
}

func TestSimulateTransactionWithSSVTrace_MailboxWrite(t *testing.T) {
	// Set up backend with mailbox address
	contracts := map[common.Address][]byte{
		mailboxA: storageCode,
	}
	backend, _ := initSSVBackendWithContracts(t, []common.Address{mailboxA}, contracts)

	// Create transaction data for write(42)
	data := common.FromHex("6057361d000000000000000000000000000000000000000000000000000000000000002a")

	tx := types.NewTransaction(0, mailboxA, big.NewInt(0), 100000, big.NewInt(params.GWei), data)
	signedTx, err := types.SignTx(tx, types.LatestSignerForChainID(backend.ChainConfig().ChainID), ssvKey)
	require.NoError(t, err)

	ctx := t.Context()
	result, err := backend.SimulateTransactionWithSSVTrace(ctx, signedTx, rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber))
	require.NoError(t, err)

	require.NotEqual(t, 0, len(result.Operations))

	foundCall := false
	foundSStore := false
	for _, op := range result.Operations {
		if op.Type == vm.CALL && op.Address == mailboxA {
			foundCall = true
		}
		if op.Type == vm.SSTORE && op.Address == mailboxA {
			foundSStore = true
			require.NotEmpty(t, op.StorageKey)
			require.NotEmpty(t, op.StorageValue)

			expectedValue := common.HexToHash("0x000000000000000000000000000000000000000000000000000000000000002a")
			require.Equal(t, expectedValue.Bytes(), op.StorageValue)
		}
	}

	require.True(t, foundCall)
	require.True(t, foundSStore)
	require.Greater(t, result.ExecutionResult.UsedGas, uint64(0))
}

func TestSimulateTransactionWithSSVTrace_MultipleMailboxes(t *testing.T) {
	contracts := map[common.Address][]byte{
		mailboxA: storageCode,
		mailboxB: storageCode,
	}
	backend, _ := initSSVBackendWithContracts(t, []common.Address{mailboxA, mailboxB}, contracts)

	// Test direct call to mailboxA (mailboxB should not be captured)
	data := common.FromHex("6057361d000000000000000000000000000000000000000000000000000000000000002a")

	tx := types.NewTransaction(0, mailboxA, big.NewInt(0), 100000, big.NewInt(params.GWei), data)
	signedTx, err := types.SignTx(tx, types.LatestSignerForChainID(backend.ChainConfig().ChainID), ssvKey)
	require.NoError(t, err)

	ctx := t.Context()
	result, err := backend.SimulateTransactionWithSSVTrace(ctx, signedTx, rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber))
	require.NoError(t, err)

	foundMailboxA := false
	foundMailboxB := false
	for _, op := range result.Operations {
		t.Logf("Operation: type=%s, address=%s, from=%s",
			op.Type.String(), op.Address.Hex(), op.From.Hex())
		if op.Address == mailboxA {
			foundMailboxA = true
		}
		if op.Address == mailboxB {
			foundMailboxB = true
		}
	}

	require.True(t, foundMailboxA)
	require.False(t, foundMailboxB)
	require.Greater(t, result.ExecutionResult.UsedGas, uint64(0))
}

func TestSimulateTransactionWithSSVTrace_FailedTransaction(t *testing.T) {
	contracts := map[common.Address][]byte{
		mailboxA: storageCode,
	}
	backend, _ := initSSVBackendWithContracts(t, []common.Address{mailboxA}, contracts)

	unfundedKey, err := crypto.GenerateKey()
	require.NoError(t, err)

	tx := types.NewTransaction(0, mailboxA, big.NewInt(1e18), 100000, big.NewInt(params.GWei), nil)
	signedTx, err := types.SignTx(tx, types.LatestSignerForChainID(backend.ChainConfig().ChainID), unfundedKey)
	require.NoError(t, err)

	ctx := t.Context()
	_, err = backend.SimulateTransactionWithSSVTrace(ctx, signedTx, rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber))
	require.Error(t, err)
}

func TestSimulateTransactionWithSSVTrace_ComplexInteractions(t *testing.T) {
	contracts := map[common.Address][]byte{
		mailboxA: storageCode,
	}
	backend, _ := initSSVBackendWithContracts(t, []common.Address{mailboxA}, contracts)

	data := common.FromHex("6057361d00000000000000000000000000000000000000000000000000000000000000ff")

	tx := types.NewTransaction(0, mailboxA, big.NewInt(0), 100000, big.NewInt(params.GWei), data)
	signedTx, err := types.SignTx(tx, types.LatestSignerForChainID(backend.ChainConfig().ChainID), ssvKey)
	require.NoError(t, err)

	ctx := t.Context()
	result, err := backend.SimulateTransactionWithSSVTrace(ctx, signedTx, rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber))
	require.NoError(t, err)

	hasMailboxOperation := false
	for _, op := range result.Operations {
		if op.Address == mailboxA {
			hasMailboxOperation = true
			t.Logf("Operation: type=%s, address=%s, from=%s",
				op.Type.String(), op.Address.Hex(), op.From.Hex())
		}
	}

	require.True(t, hasMailboxOperation)
	require.False(t, result.ExecutionResult.Failed())
	require.Greater(t, result.ExecutionResult.UsedGas, uint64(0))
}
