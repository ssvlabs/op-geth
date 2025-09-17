// Copyright 2022 The go-ethereum Authors
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

package miner

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/beacon/engine"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/clique"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/txpool/legacypool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

var (
	// Test chain configurations
	testTxPoolConfig  legacypool.Config
	ethashChainConfig *params.ChainConfig
	cliqueChainConfig *params.ChainConfig

	// Test accounts
	testBankKey, _  = crypto.GenerateKey()
	testBankAddress = crypto.PubkeyToAddress(testBankKey.PublicKey)
	testBankFunds   = big.NewInt(1000000000000000000)

	testUserKey, _  = crypto.GenerateKey()
	testUserAddress = crypto.PubkeyToAddress(testUserKey.PublicKey)

	testRecipient = common.HexToAddress("0xdeadbeef")
	testTimestamp = uint64(time.Now().Unix())

	// Test transactions
	pendingTxs []*types.Transaction
	newTxs     []*types.Transaction

	testConfig = Config{
		PendingFeeRecipient: testBankAddress,
		Recommit:            time.Second,
		GasCeil:             50_000_000,
	}
)

const (
	numDAFilterTxs = 256
)

var zero = uint64(0)

func init() {
	testTxPoolConfig = legacypool.DefaultConfig
	testTxPoolConfig.Journal = ""
	ethashChainConfig = new(params.ChainConfig)
	*ethashChainConfig = *params.TestChainConfig
	cliqueChainConfig = new(params.ChainConfig)
	*cliqueChainConfig = *params.TestChainConfig
	cliqueChainConfig.Clique = &params.CliqueConfig{
		Period: 10,
		Epoch:  30000,
	}

	signer := types.LatestSigner(params.TestChainConfig)
	tx1 := types.MustSignNewTx(testBankKey, signer, &types.AccessListTx{
		ChainID:  params.TestChainConfig.ChainID,
		Nonce:    0,
		To:       &testUserAddress,
		Value:    big.NewInt(1000),
		Gas:      params.TxGas,
		GasPrice: big.NewInt(params.InitialBaseFee),
	})
	pendingTxs = append(pendingTxs, tx1)

	tx2 := types.MustSignNewTx(testBankKey, signer, &types.LegacyTx{
		Nonce:    1,
		To:       &testUserAddress,
		Value:    big.NewInt(1000),
		Gas:      params.TxGas,
		GasPrice: big.NewInt(params.InitialBaseFee),
	})
	newTxs = append(newTxs, tx2)
}

// testWorkerBackend implements worker.Backend interfaces and wraps all information needed during the testing.
type testWorkerBackend struct {
	db      ethdb.Database
	txPool  *txpool.TxPool
	chain   *core.BlockChain
	genesis *core.Genesis
}

func newTestWorkerBackend(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine, db ethdb.Database, n int) *testWorkerBackend {
	var gspec = &core.Genesis{
		Config: chainConfig,
		Alloc:  types.GenesisAlloc{testBankAddress: {Balance: testBankFunds}},
	}
	switch e := engine.(type) {
	case *clique.Clique:
		gspec.ExtraData = make([]byte, 32+common.AddressLength+crypto.SignatureLength)
		copy(gspec.ExtraData[32:32+common.AddressLength], testBankAddress.Bytes())
		e.Authorize(testBankAddress)
	case *ethash.Ethash:
	default:
		t.Fatalf("unexpected consensus engine type: %T", engine)
	}
	if chainConfig.HoloceneTime != nil {
		minBaseFee := uint64(0)
		gspec.ExtraData = eip1559.EncodeOptimismExtraData(chainConfig, *chainConfig.HoloceneTime, 250, 6, &minBaseFee)
	}
	chain, err := core.NewBlockChain(db, gspec, engine, &core.BlockChainConfig{ArchiveMode: true})
	if err != nil {
		t.Fatalf("core.NewBlockChain failed: %v", err)
	}
	pool := legacypool.New(testTxPoolConfig, chain)
	txpool, _ := txpool.New(testTxPoolConfig.PriceLimit, chain, []txpool.SubPool{pool}, nil)

	return &testWorkerBackend{
		db:      db,
		chain:   chain,
		txPool:  txpool,
		genesis: gspec,
	}
}

func (b *testWorkerBackend) BlockChain() *core.BlockChain { return b.chain }
func (b *testWorkerBackend) TxPool() *txpool.TxPool       { return b.txPool }

func newTestWorker(t *testing.T, chainConfig *params.ChainConfig, engine consensus.Engine, db ethdb.Database, blocks int) (*Miner, *testWorkerBackend) {
	backend := newTestWorkerBackend(t, chainConfig, engine, db, blocks)
	backend.txPool.Add(pendingTxs, true)
	w := New(backend, testConfig, engine)
	return w, backend
}

func TestBuildPayload(t *testing.T) {
	t.Run("no-tx-pool", func(t *testing.T) { testBuildPayload(t, true, false, nil, params.TestChainConfig) })
	// no-tx-pool case with interrupt not interesting because no-tx-pool doesn't run
	// the builder routine
	t.Run("with-tx-pool", func(t *testing.T) { testBuildPayload(t, false, false, nil, params.TestChainConfig) })
	t.Run("with-tx-pool-interrupt", func(t *testing.T) { testBuildPayload(t, false, true, nil, params.TestChainConfig) })
	params1559 := []byte{0, 1, 2, 3, 4, 5, 6, 7}
	t.Run("with-params-holocene", func(t *testing.T) { testBuildPayload(t, false, false, params1559, holoceneConfig()) })
	t.Run("with-params-no-tx-pool-holocene", func(t *testing.T) { testBuildPayload(t, true, false, params1559, holoceneConfig()) })
	t.Run("with-params-interrupt-holocene", func(t *testing.T) { testBuildPayload(t, false, true, params1559, holoceneConfig()) })
	t.Run("with-params-jovian", func(t *testing.T) { testBuildPayload(t, false, false, params1559, jovianConfig()) })
	t.Run("with-params-no-tx-pool-jovian", func(t *testing.T) { testBuildPayload(t, true, false, params1559, jovianConfig()) })
	t.Run("with-params-interrupt-jovian", func(t *testing.T) { testBuildPayload(t, false, true, params1559, jovianConfig()) })

	t.Run("wrong-config-no-params", func(t *testing.T) { testBuildPayloadWrongConfig(t, nil, holoceneConfig()) })
	t.Run("wrong-config-params-holocene", func(t *testing.T) { testBuildPayloadWrongConfig(t, params1559, holoceneConfig()) })
	t.Run("wrong-config-params-jovian", func(t *testing.T) { testBuildPayloadWrongConfig(t, params1559, jovianConfig()) })

	zeroParams := make([]byte, 8)
	t.Run("with-zero-params-holocene", func(t *testing.T) { testBuildPayload(t, true, false, zeroParams, holoceneConfig()) })
	t.Run("with-zero-params-jovian", func(t *testing.T) { testBuildPayload(t, true, false, zeroParams, jovianConfig()) })
}

func TestDAFilters(t *testing.T) {
	// Each test case inserts one pending small (DA cost 100) transaction followed by
	// numDAFilterTxs transactions that have random calldata (min DA size >> 100)
	totalTxs := numDAFilterTxs + 1

	// Very low max should filter all transactions.
	t.Run("with-tx-filter-max-filters-all", func(t *testing.T) { testDAFilters(t, big.NewInt(1), nil, 0) })
	t.Run("with-block-filter-max-filters-all", func(t *testing.T) { testDAFilters(t, nil, big.NewInt(1), 0) })
	// Very high max should filter nothing.
	t.Run("with-tx-filter-max-too-high", func(t *testing.T) { testDAFilters(t, big.NewInt(1000000), nil, totalTxs) })
	t.Run("with-block-filter-max-too-high", func(t *testing.T) { testDAFilters(t, nil, big.NewInt(1000000), totalTxs) })
	// The first transaction has size 100, all other DA test txs are bigger due to random Data, so should get filtered.
	t.Run("with-tx-filter-all-but-first", func(t *testing.T) { testDAFilters(t, big.NewInt(100), nil, 1) })
	t.Run("with-block-filter-all-but-first", func(t *testing.T) { testDAFilters(t, nil, big.NewInt(100), 1) })
	// Zero/nil values for these parameters means we should never filter
	t.Run("with-zero-tx-filters", func(t *testing.T) { testDAFilters(t, big.NewInt(0), big.NewInt(0), totalTxs) })
	t.Run("with-nil-tx-filters", func(t *testing.T) { testDAFilters(t, nil, nil, totalTxs) })
}

func holoceneConfig() *params.ChainConfig {
	config := *params.TestChainConfig
	config.LondonBlock = big.NewInt(0)
	t := uint64(0)
	config.CanyonTime = &t
	config.HoloceneTime = &t
	canyonDenom := uint64(250)
	config.Optimism = &params.OptimismConfig{
		EIP1559Elasticity:        6,
		EIP1559Denominator:       50,
		EIP1559DenominatorCanyon: &canyonDenom,
	}
	return &config
}

func jovianConfig() *params.ChainConfig {
	config := holoceneConfig()
	zero := uint64(0)
	config.JovianTime = &zero
	return config
}

// newPayloadArgs returns a BuildPaylooadArgs with the given parentHash, eip-1559 params,
// minBaseFee, testTimestamp for Timestamp, and testRecipient for recipient. NoTxPool is set to true.
func newPayloadArgs(parentHash common.Hash, params1559 []byte, minBaseFee *uint64) *BuildPayloadArgs {
	return &BuildPayloadArgs{
		Parent:        parentHash,
		Timestamp:     testTimestamp,
		Random:        common.Hash{},
		FeeRecipient:  testRecipient,
		NoTxPool:      true,
		EIP1559Params: params1559,
		MinBaseFee:    minBaseFee,
	}
}

func testBuildPayload(t *testing.T, noTxPool, interrupt bool, params1559 []byte, config *params.ChainConfig) {
	t.Parallel()
	db := rawdb.NewMemoryDatabase()

	var minBaseFee *uint64
	if config.IsMinBaseFee(testTimestamp) {
		val := uint64(1e9)
		minBaseFee = &val
	}
	w, b := newTestWorker(t, config, ethash.NewFaker(), db, 0)

	const numInterruptTxs = 256

	if interrupt {
		// when doing interrupt testing, create a large pool so interruption will
		// definitely be visible.
		txs := genTxs(1, numInterruptTxs)
		b.txPool.Add(txs, false)
	}

	args := newPayloadArgs(b.chain.CurrentBlock().Hash(), params1559, minBaseFee)
	args.NoTxPool = noTxPool

	// payload resolution now interrupts block building, so we have to
	// wait for the payloading building process to build its first block
	payload, err := w.buildPayload(args, false)
	if err != nil {
		t.Fatalf("Failed to build payload %v", err)
	}
	verify := func(outer *engine.ExecutionPayloadEnvelope, txs int) {
		t.Helper()
		if outer == nil {
			t.Fatal("ExecutionPayloadEnvelope is nil")
		}
		payload := outer.ExecutionPayload
		if payload.ParentHash != b.chain.CurrentBlock().Hash() {
			t.Fatal("Unexpected parent hash")
		}
		if payload.Random != (common.Hash{}) {
			t.Fatal("Unexpected random value")
		}
		if payload.Timestamp != testTimestamp {
			t.Fatal("Unexpected timestamp")
		}
		if payload.FeeRecipient != testRecipient {
			t.Fatal("Unexpected fee recipient")
		}
		if !interrupt && len(payload.Transactions) != txs {
			t.Fatalf("Unexpect transaction set: got %d, expected %d", len(payload.Transactions), txs)
		} else if interrupt && len(payload.Transactions) >= txs {
			t.Fatalf("Unexpect transaction set: got %d, expected less than %d", len(payload.Transactions), txs)
		}
	}
	// OP-Stack: we only build the empty payload if noTxPool is set.
	if args.NoTxPool {
		empty := payload.ResolveEmpty()
		verify(empty, 0)
	}

	// make sure the 1559 params we've specied (if any) ends up in both the full and empty block headers
	var expected []byte
	if len(params1559) != 0 {
		versionByte := eip1559.HoloceneExtraDataVersionByte
		if config.IsMinBaseFee(testTimestamp) {
			versionByte = eip1559.MinBaseFeeExtraDataVersionByte
		}
		expected = []byte{versionByte}

		d, _ := eip1559.DecodeHolocene1559Params(params1559)
		if d == 0 {
			expected = append(expected, eip1559.EncodeHolocene1559Params(250, 6)...) // canyon defaults
		} else {
			expected = append(expected, params1559...)
		}
		if versionByte == eip1559.MinBaseFeeExtraDataVersionByte {
			buf := make([]byte, 8)
			binary.BigEndian.PutUint64(buf, *minBaseFee)
			expected = append(expected, buf...)
		}
	}
	if payload.full != nil && !bytes.Equal(payload.full.Header().Extra, expected) {
		t.Fatalf("ExtraData doesn't match. want: %x, got %x", expected, payload.full.Header().Extra)
	}
	if payload.empty != nil && !bytes.Equal(payload.empty.Header().Extra, expected) {
		t.Fatalf("ExtraData doesn't match on empty block. want: %x, got %x", expected, payload.empty.Header().Extra)
	}

	// Test extraData
	if payload.full != nil && len(params1559) != 0 {
		d, e, extractedMinBaseFee := eip1559.DecodeOptimismExtraData(config, testTimestamp, payload.full.Header().Extra)

		expectedDenominator := binary.BigEndian.Uint32(params1559[:4])
		expectedElasticity := binary.BigEndian.Uint32(params1559[4:])
		if expectedDenominator == 0 {
			expectedDenominator = 250
			expectedElasticity = 6
		}
		if d != uint64(expectedDenominator) {
			t.Fatalf("denominator doesn't match. want: %d, got %d", expectedDenominator, d)
		}
		if e != uint64(expectedElasticity) {
			t.Fatalf("elasticity doesn't match. want: %d, got %d", expectedElasticity, e)
		}
		require.Equal(t, minBaseFee, extractedMinBaseFee, "minBaseFee doesn't match")
	}

	if noTxPool {
		// we only build the empty block when ignoring the tx pool
		empty := payload.ResolveEmpty()
		verify(empty, 0)
		full := payload.ResolveFull()
		verify(full, 0)
	} else if interrupt {
		full := payload.ResolveFull()
		verify(full, len(pendingTxs)+numInterruptTxs)
	} else { // tx-pool and no interrupt
		payload.WaitFull()
		full := payload.ResolveFull()
		verify(full, len(pendingTxs))
	}

	// Ensure resolve can be called multiple times and the
	// result should be unchanged
	dataOne := payload.Resolve()
	dataTwo := payload.Resolve()
	if !reflect.DeepEqual(dataOne, dataTwo) {
		t.Fatal("Unexpected payload data")
	}
}

func testDAFilters(t *testing.T, maxDATxSize, maxDABlockSize *big.Int, expectedTxCount int) {
	t.Parallel()
	db := rawdb.NewMemoryDatabase()
	config := holoceneConfig()
	w, b := newTestWorker(t, config, ethash.NewFaker(), db, 0)
	w.SetMaxDASize(maxDATxSize, maxDABlockSize)
	txs := genTxs(1, numDAFilterTxs)
	b.txPool.Add(txs, false)

	params1559 := []byte{0, 1, 2, 3, 4, 5, 6, 7}
	args := newPayloadArgs(b.chain.CurrentBlock().Hash(), params1559, &zero)
	args.NoTxPool = false

	payload, err := w.buildPayload(args, false)
	if err != nil {
		t.Fatalf("Failed to build payload %v", err)
	}
	payload.WaitFull()
	result := payload.ResolveFull().ExecutionPayload
	if len(result.Transactions) != expectedTxCount {
		t.Fatalf("Unexpected transaction set: got %d, expected %d", len(result.Transactions), expectedTxCount)
	}
}

func testBuildPayloadWrongConfig(t *testing.T, params1559 []byte, config *params.ChainConfig) {
	t.Parallel()
	db := rawdb.NewMemoryDatabase()
	wrongConfig := *config
	if len(params1559) != 0 {
		// deactivate holocene and jovian and make sure non-empty params get rejected
		wrongConfig.HoloceneTime = nil
		wrongConfig.JovianTime = nil
	}
	w, b := newTestWorker(t, &wrongConfig, ethash.NewFaker(), db, 0)

	args := newPayloadArgs(b.chain.CurrentBlock().Hash(), params1559, &zero)
	payload, err := w.buildPayload(args, false)
	if err == nil && (payload == nil || payload.err == nil) {
		t.Fatalf("expected error, got none")
	}
}

func TestBuildPayloadInvalidHoloceneParams(t *testing.T) {
	t.Parallel()
	db := rawdb.NewMemoryDatabase()
	config := holoceneConfig()
	w, b := newTestWorker(t, config, ethash.NewFaker(), db, 0)

	// 0 denominators shouldn't be allowed
	badParams := eip1559.EncodeHolocene1559Params(0, 6)

	args := newPayloadArgs(b.chain.CurrentBlock().Hash(), badParams, &zero)
	payload, err := w.buildPayload(args, false)
	if err == nil && (payload == nil || payload.err == nil) {
		t.Fatalf("expected error, got none")
	}
}

func TestBuildPayloadInvalidMinBaseFeeExtraData(t *testing.T) {
	t.Parallel()
	db := rawdb.NewMemoryDatabase()
	config := jovianConfig()
	w, b := newTestWorker(t, config, ethash.NewFaker(), db, 0)

	// 0 denominators shouldn't be allowed
	badParams := eip1559.EncodeMinBaseFeeExtraData(0, 6, 0)

	args := newPayloadArgs(b.chain.CurrentBlock().Hash(), badParams, &zero)
	payload, err := w.buildPayload(args, false)
	if err == nil && (payload == nil || payload.err == nil) {
		t.Fatalf("expected error, got none")
	}

	// missing minBaseFee shouldn't be allowed (use Holocene encoder)
	badParams = eip1559.EncodeHoloceneExtraData(250, 6)
	args = newPayloadArgs(b.chain.CurrentBlock().Hash(), badParams, &zero)
	payload, err = w.buildPayload(args, false)
	if err == nil && (payload == nil || payload.err == nil) {
		t.Fatalf("expected error, got none")
	}
}

func genTxs(startNonce, count uint64) types.Transactions {
	txs := make(types.Transactions, 0, count)
	signer := types.LatestSigner(params.TestChainConfig)
	for nonce := startNonce; nonce < startNonce+count; nonce++ {
		// generate incompressible data to put in the tx for DA filter testing. each of these
		// txs will be bigger than the 100 minimum.
		randomBytes := make([]byte, 100)
		_, err := rand.Read(randomBytes)
		if err != nil {
			panic(err)
		}
		tx := types.MustSignNewTx(testBankKey, signer, &types.AccessListTx{
			ChainID:  params.TestChainConfig.ChainID,
			Nonce:    nonce,
			To:       &testUserAddress,
			Value:    big.NewInt(1000),
			Gas:      params.TxGas + uint64(len(randomBytes))*16,
			GasPrice: big.NewInt(params.InitialBaseFee),
			Data:     randomBytes,
		})
		txs = append(txs, tx)
	}
	return txs
}

func TestPayloadId(t *testing.T) {
	t.Parallel()
	ids := make(map[string]int)
	for i, tt := range []*BuildPayloadArgs{
		{
			Parent:       common.Hash{1},
			Timestamp:    1,
			Random:       common.Hash{0x1},
			FeeRecipient: common.Address{0x1},
		},
		// Different parent
		{
			Parent:       common.Hash{2},
			Timestamp:    1,
			Random:       common.Hash{0x1},
			FeeRecipient: common.Address{0x1},
		},
		// Different timestamp
		{
			Parent:       common.Hash{2},
			Timestamp:    2,
			Random:       common.Hash{0x1},
			FeeRecipient: common.Address{0x1},
		},
		// Different Random
		{
			Parent:       common.Hash{2},
			Timestamp:    2,
			Random:       common.Hash{0x2},
			FeeRecipient: common.Address{0x1},
		},
		// Different fee-recipient
		{
			Parent:       common.Hash{2},
			Timestamp:    2,
			Random:       common.Hash{0x2},
			FeeRecipient: common.Address{0x2},
		},
		// Different withdrawals (non-empty)
		{
			Parent:       common.Hash{2},
			Timestamp:    2,
			Random:       common.Hash{0x2},
			FeeRecipient: common.Address{0x2},
			Withdrawals: []*types.Withdrawal{
				{
					Index:     0,
					Validator: 0,
					Address:   common.Address{},
					Amount:    0,
				},
			},
		},
		// Different withdrawals (non-empty)
		{
			Parent:       common.Hash{2},
			Timestamp:    2,
			Random:       common.Hash{0x2},
			FeeRecipient: common.Address{0x2},
			Withdrawals: []*types.Withdrawal{
				{
					Index:     2,
					Validator: 0,
					Address:   common.Address{},
					Amount:    0,
				},
			},
		},
		{
			Parent:       common.Hash{2},
			Timestamp:    2,
			Random:       common.Hash{0x2},
			FeeRecipient: common.Address{0x2},
			MinBaseFee:   &zero,
		},
	} {
		id := tt.Id().String()
		if prev, exists := ids[id]; exists {
			t.Errorf("ID collision, case %d and case %d: id %v", prev, i, id)
		}
		ids[id] = i
	}
}

// OPStack addition
func TestDeterministicPayloadId(t *testing.T) {
	makeArgs := func() *BuildPayloadArgs {
		val := uint64(5)
		return &BuildPayloadArgs{
			Parent:       common.Hash{2},
			Timestamp:    2,
			Random:       common.Hash{0x2},
			FeeRecipient: common.Address{0x2},
			MinBaseFee:   &val,
		}
	}

	id1 := makeArgs().Id().String()
	id2 := makeArgs().Id().String()
	require.Equal(t, id1, id2)
}
