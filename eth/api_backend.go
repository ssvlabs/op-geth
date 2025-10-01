// Copyright 2015 The go-ethereum Authors
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
	"bytes"
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/crypto"
	rollupv1 "github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/proto/rollup/v1"
	"github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/x/superblock/sequencer/xt"
	"github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/x/transport"

	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/core/ssv"
	"github.com/ethereum/go-ethereum/eth/tracers/native"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/misc/eip4844"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/filtermaps"
	"github.com/ethereum/go-ethereum/core/history"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/txpool/locals"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/eth/gasprice"
	"github.com/ethereum/go-ethereum/eth/tracers"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"

	spconsensus "github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/x/consensus"
	"github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/x/superblock/sequencer"
)

// EthAPIBackend implements ethapi.Backend and tracers.Backend for full nodes
type EthAPIBackend struct {
	extRPCEnabled       bool
	allowUnprotectedTxs bool
	disableTxPool       bool
	eth                 *Ethereum
	gpo                 *gasprice.Oracle

	// SSV: Shared publisher + SBCP coordinator integration
	spClient         transport.Client
	coordinator      sequencer.Coordinator
	sequencerClients map[string]transport.Client
	sequencerKey     *ecdsa.PrivateKey
	sequencerAddress common.Address
	mailboxAddresses []common.Address
	mailboxByChainID map[uint64]common.Address

	// SSV: Sequencer transaction management
	pendingPutInboxTxs  []*types.Transaction
	pendingSequencerTxs []*types.Transaction
	sequencerTxMutex    sync.RWMutex

	// SSV: Track XT results for forwarded RPC requests
	xtTracker *xt.XTResultTracker

	// SSV: Track last RequestSeal inclusion list for SBCP
	rsMutex                 sync.RWMutex
	lastRequestSealIncluded [][]byte
	lastRequestSealSlot     uint64

	// SSV: Store built blocks to send after RequestSeal
	pendingBlockMutex sync.RWMutex
	pendingBlock      *types.Block
	pendingBlockSlot  uint64
}

// ChainConfig returns the active chain configuration.
func (b *EthAPIBackend) ChainConfig() *params.ChainConfig {
	return b.eth.blockchain.Config()
}

func (b *EthAPIBackend) CurrentBlock() *types.Header {
	return b.eth.blockchain.CurrentBlock()
}

func (b *EthAPIBackend) SetHead(number uint64) {
	b.eth.handler.downloader.Cancel()
	b.eth.blockchain.SetHead(number)
}

func (b *EthAPIBackend) HeaderByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Header, error) {
	// Pending block is only known by the miner
	if number == rpc.PendingBlockNumber {
		block, _, _ := b.eth.miner.Pending(ctx)
		if block == nil {
			return nil, errors.New("pending block is not available")
		}
		return block.Header(), nil
	}
	// Otherwise resolve and return the block
	if number == rpc.LatestBlockNumber {
		return b.eth.blockchain.CurrentBlock(), nil
	}
	if number == rpc.FinalizedBlockNumber {
		block := b.eth.blockchain.CurrentFinalBlock()
		if block == nil {
			return nil, errors.New("finalized block not found")
		}
		return block, nil
	}
	if number == rpc.SafeBlockNumber {
		block := b.eth.blockchain.CurrentSafeBlock()
		if block == nil {
			return nil, errors.New("safe block not found")
		}
		return block, nil
	}
	var bn uint64
	if number == rpc.EarliestBlockNumber {
		bn = b.HistoryPruningCutoff()
	} else {
		bn = uint64(number)
	}
	return b.eth.blockchain.GetHeaderByNumber(bn), nil
}

func (b *EthAPIBackend) HeaderByNumberOrHash(
	ctx context.Context,
	blockNrOrHash rpc.BlockNumberOrHash,
) (*types.Header, error) {
	if blockNr, ok := blockNrOrHash.Number(); ok {
		return b.HeaderByNumber(ctx, blockNr)
	}
	if hash, ok := blockNrOrHash.Hash(); ok {
		header := b.eth.blockchain.GetHeaderByHash(hash)
		if header == nil {
			return nil, errors.New("header for hash not found")
		}
		if blockNrOrHash.RequireCanonical && b.eth.blockchain.GetCanonicalHash(header.Number.Uint64()) != hash {
			return nil, errors.New("hash is not currently canonical")
		}
		return header, nil
	}
	return nil, errors.New("invalid arguments; neither block nor hash specified")
}

func (b *EthAPIBackend) HeaderByHash(ctx context.Context, hash common.Hash) (*types.Header, error) {
	return b.eth.blockchain.GetHeaderByHash(hash), nil
}

func (b *EthAPIBackend) BlockByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Block, error) {
	// Pending block is only known by the miner
	if number == rpc.PendingBlockNumber {
		block, _, _ := b.eth.miner.Pending(context.Background())
		if block == nil {
			return nil, errors.New("pending block is not available")
		}
		return block, nil
	}
	// Otherwise resolve and return the block
	if number == rpc.LatestBlockNumber {
		header := b.eth.blockchain.CurrentBlock()
		return b.eth.blockchain.GetBlock(header.Hash(), header.Number.Uint64()), nil
	}
	if number == rpc.FinalizedBlockNumber {
		header := b.eth.blockchain.CurrentFinalBlock()
		if header == nil {
			return nil, errors.New("finalized block not found")
		}
		return b.eth.blockchain.GetBlock(header.Hash(), header.Number.Uint64()), nil
	}
	if number == rpc.SafeBlockNumber {
		header := b.eth.blockchain.CurrentSafeBlock()
		if header == nil {
			return nil, errors.New("safe block not found")
		}
		return b.eth.blockchain.GetBlock(header.Hash(), header.Number.Uint64()), nil
	}
	bn := uint64(number) // the resolved number
	if number == rpc.EarliestBlockNumber {
		bn = b.HistoryPruningCutoff()
	}
	block := b.eth.blockchain.GetBlockByNumber(bn)
	if block == nil && bn < b.HistoryPruningCutoff() {
		return nil, &history.PrunedHistoryError{}
	}
	return block, nil
}

func (b *EthAPIBackend) BlockByHash(ctx context.Context, hash common.Hash) (*types.Block, error) {
	number := b.eth.blockchain.GetBlockNumber(hash)
	if number == nil {
		return nil, nil
	}
	block := b.eth.blockchain.GetBlock(hash, *number)
	if block == nil && *number < b.HistoryPruningCutoff() {
		return nil, &history.PrunedHistoryError{}
	}
	return block, nil
}

// GetBody returns body of a block. It does not resolve special block numbers.
func (b *EthAPIBackend) GetBody(ctx context.Context, hash common.Hash, number rpc.BlockNumber) (*types.Body, error) {
	if number < 0 || hash == (common.Hash{}) {
		return nil, errors.New("invalid arguments; expect hash and no special block numbers")
	}
	body := b.eth.blockchain.GetBody(hash)
	if body == nil {
		if uint64(number) < b.HistoryPruningCutoff() {
			return nil, &history.PrunedHistoryError{}
		}
		return nil, errors.New("block body not found")
	}
	return body, nil
}

func (b *EthAPIBackend) BlockByNumberOrHash(
	ctx context.Context,
	blockNrOrHash rpc.BlockNumberOrHash,
) (*types.Block, error) {
	if blockNr, ok := blockNrOrHash.Number(); ok {
		return b.BlockByNumber(ctx, blockNr)
	}
	if hash, ok := blockNrOrHash.Hash(); ok {
		header := b.eth.blockchain.GetHeaderByHash(hash)
		if header == nil {
			return nil, errors.New("header for hash not found")
		}
		if blockNrOrHash.RequireCanonical && b.eth.blockchain.GetCanonicalHash(header.Number.Uint64()) != hash {
			return nil, errors.New("hash is not currently canonical")
		}
		block := b.eth.blockchain.GetBlock(hash, header.Number.Uint64())
		if block == nil {
			if header.Number.Uint64() < b.HistoryPruningCutoff() {
				return nil, &history.PrunedHistoryError{}
			}
			return nil, errors.New("header found, but block body is missing")
		}
		return block, nil
	}
	return nil, errors.New("invalid arguments; neither block nor hash specified")
}

func (b *EthAPIBackend) Pending() (*types.Block, types.Receipts, *state.StateDB) {
	return b.eth.miner.Pending(context.Background())
}

func (b *EthAPIBackend) StateAndHeaderByNumber(
	ctx context.Context,
	number rpc.BlockNumber,
) (*state.StateDB, *types.Header, error) {
	// Pending state is only known by the miner
	if number == rpc.PendingBlockNumber {
		block, _, state := b.eth.miner.Pending(ctx)
		if block != nil && state != nil {
			//state.TxIndex() == 1
			//sequencerBalance := state.GetBalance(common.HexToAddress("0x0f10aF865F68F5aA1dDB7c5b5A1a0f396232C6Be"))
			//fmt.Println("[AFTER] Sequencer balance: ", sequencerBalance.String())
			return state, block.Header(), nil
		} else {
			number = rpc.LatestBlockNumber // fall back to latest state
		}
	}
	// Otherwise resolve the block number and return its state
	header, err := b.HeaderByNumber(ctx, number)
	if err != nil {
		return nil, nil, err
	}
	if header == nil {
		return nil, nil, fmt.Errorf("header %w", ethereum.NotFound)
	}
	stateDb, err := b.eth.BlockChain().StateAt(header.Root)
	if err != nil {
		return nil, nil, err
	}
	return stateDb, header, nil
}

func (b *EthAPIBackend) StateAndHeaderByNumberOrHash(
	ctx context.Context,
	blockNrOrHash rpc.BlockNumberOrHash,
) (*state.StateDB, *types.Header, error) {
	if blockNr, ok := blockNrOrHash.Number(); ok {
		return b.StateAndHeaderByNumber(ctx, blockNr)
	}
	if hash, ok := blockNrOrHash.Hash(); ok {
		header, err := b.HeaderByHash(ctx, hash)
		if err != nil {
			return nil, nil, err
		}
		if header == nil {
			return nil, nil, fmt.Errorf("header for hash %w", ethereum.NotFound)
		}
		if blockNrOrHash.RequireCanonical && b.eth.blockchain.GetCanonicalHash(header.Number.Uint64()) != hash {
			return nil, nil, errors.New("hash is not currently canonical")
		}
		stateDb, err := b.eth.BlockChain().StateAt(header.Root)
		if err != nil {
			return nil, nil, err
		}
		return stateDb, header, nil
	}
	return nil, nil, errors.New("invalid arguments; neither block nor hash specified")
}

func (b *EthAPIBackend) HistoryPruningCutoff() uint64 {
	bn, _ := b.eth.blockchain.HistoryPruningCutoff()
	return bn
}

func (b *EthAPIBackend) GetReceipts(ctx context.Context, hash common.Hash) (types.Receipts, error) {
	return b.eth.blockchain.GetReceiptsByHash(hash), nil
}

func (b *EthAPIBackend) GetLogs(ctx context.Context, hash common.Hash, number uint64) ([][]*types.Log, error) {
	return rawdb.ReadLogs(b.eth.chainDb, hash, number), nil
}

func (b *EthAPIBackend) GetEVM(
	ctx context.Context,
	state *state.StateDB,
	header *types.Header,
	vmConfig *vm.Config,
	blockCtx *vm.BlockContext,
) *vm.EVM {
	if vmConfig == nil {
		vmConfig = b.eth.blockchain.GetVMConfig()
	}
	var context vm.BlockContext
	if blockCtx != nil {
		context = *blockCtx
	} else {
		context = core.NewEVMBlockContext(header, b.eth.BlockChain(), nil, b.eth.blockchain.Config(), state)
	}
	return vm.NewEVM(context, state, b.ChainConfig(), *vmConfig)
}

func (b *EthAPIBackend) SubscribeRemovedLogsEvent(ch chan<- core.RemovedLogsEvent) event.Subscription {
	return b.eth.BlockChain().SubscribeRemovedLogsEvent(ch)
}

func (b *EthAPIBackend) SubscribeChainEvent(ch chan<- core.ChainEvent) event.Subscription {
	return b.eth.BlockChain().SubscribeChainEvent(ch)
}

func (b *EthAPIBackend) SubscribeChainHeadEvent(ch chan<- core.ChainHeadEvent) event.Subscription {
	return b.eth.BlockChain().SubscribeChainHeadEvent(ch)
}

func (b *EthAPIBackend) SubscribeLogsEvent(ch chan<- []*types.Log) event.Subscription {
	return b.eth.BlockChain().SubscribeLogsEvent(ch)
}

func (b *EthAPIBackend) SendTx(ctx context.Context, signedTx *types.Transaction) error {
	if b.ChainConfig().IsOptimism() && signedTx.Type() == types.BlobTxType {
		return types.ErrTxTypeNotSupported
	}

	// OP-Stack: forward to remote sequencer RPC
	if b.eth.seqRPCService != nil {
		data, err := signedTx.MarshalBinary()
		if err != nil {
			return err
		}
		if err := b.eth.seqRPCService.CallContext(ctx, nil, "eth_sendRawTransaction", hexutil.Encode(data)); err != nil {
			return err
		}
	}
	if b.disableTxPool {
		return nil
	}

	// Retain tx in local tx pool after forwarding, for local RPC usage.
	err := b.sendTx(ctx, signedTx)
	if err != nil && b.eth.seqRPCService != nil {
		log.Warn(
			"successfully sent tx to sequencer, but failed to persist in local tx pool",
			"err",
			err,
			"tx",
			signedTx.Hash(),
		)
		return nil
	}
	return err
}

func (b *EthAPIBackend) sendTx(ctx context.Context, signedTx *types.Transaction) error {
	err := b.eth.txPool.Add([]*types.Transaction{signedTx}, false)[0]

	// If the local transaction tracker is not configured, returns whatever
	// returned from the txpool.
	if b.eth.localTxTracker == nil {
		return err
	}
	// If the transaction fails with an error indicating it is invalid, or if there is
	// very little chance it will be accepted later (e.g., the gas price is below the
	// configured minimum, or the sender has insufficient funds to cover the cost),
	// propagate the error to the user.
	if err != nil && !locals.IsTemporaryReject(err) {
		return err
	}
	// No error will be returned to user if the transaction fails with a temporary
	// error and might be accepted later (e.g., the transaction pool is full).
	// Locally submitted transactions will be resubmitted later via the local tracker.
	b.eth.localTxTracker.Track(signedTx)
	return nil
}

func (b *EthAPIBackend) GetPoolTransactions() (types.Transactions, error) {
	pending := b.eth.txPool.Pending(txpool.PendingFilter{})
	var txs types.Transactions
	for _, batch := range pending {
		for _, lazy := range batch {
			if tx := lazy.Resolve(); tx != nil {
				txs = append(txs, tx)
			}
		}
	}
	return txs, nil
}

func (b *EthAPIBackend) GetPoolTransaction(hash common.Hash) *types.Transaction {
	return b.eth.txPool.Get(hash)
}

// GetTransaction retrieves the lookup along with the transaction itself associate
// with the given transaction hash.
//
// A null will be returned if the transaction is not found. The transaction is not
// existent from the node's perspective. This can be due to the transaction indexer
// not being finished. The caller must explicitly check the indexer progress.
func (b *EthAPIBackend) GetTransaction(txHash common.Hash) (bool, *types.Transaction, common.Hash, uint64, uint64) {
	lookup, tx := b.eth.blockchain.GetTransactionLookup(txHash)
	if lookup == nil || tx == nil {
		return false, nil, common.Hash{}, 0, 0
	}
	return true, tx, lookup.BlockHash, lookup.BlockIndex, lookup.Index
}

// TxIndexDone returns true if the transaction indexer has finished indexing.
func (b *EthAPIBackend) TxIndexDone() bool {
	return b.eth.blockchain.TxIndexDone()
}

func (b *EthAPIBackend) GetPoolNonce(ctx context.Context, addr common.Address) (uint64, error) {
	return b.eth.txPool.PoolNonce(addr), nil
}

func (b *EthAPIBackend) Stats() (runnable int, blocked int) {
	return b.eth.txPool.Stats()
}

func (b *EthAPIBackend) TxPoolContent() (map[common.Address][]*types.Transaction, map[common.Address][]*types.Transaction) {
	return b.eth.txPool.Content()
}

func (b *EthAPIBackend) TxPoolContentFrom(addr common.Address) ([]*types.Transaction, []*types.Transaction) {
	return b.eth.txPool.ContentFrom(addr)
}

func (b *EthAPIBackend) TxPool() *txpool.TxPool {
	return b.eth.txPool
}

func (b *EthAPIBackend) SubscribeNewTxsEvent(ch chan<- core.NewTxsEvent) event.Subscription {
	return b.eth.txPool.SubscribeTransactions(ch, true)
}

func (b *EthAPIBackend) SyncProgress(ctx context.Context) ethereum.SyncProgress {
	prog := b.eth.Downloader().Progress()
	if txProg, err := b.eth.blockchain.TxIndexProgress(); err == nil {
		prog.TxIndexFinishedBlocks = txProg.Indexed
		prog.TxIndexRemainingBlocks = txProg.Remaining
	}
	return prog
}

func (b *EthAPIBackend) SuggestGasTipCap(ctx context.Context) (*big.Int, error) {
	return b.gpo.SuggestTipCap(ctx)
}

func (b *EthAPIBackend) FeeHistory(
	ctx context.Context,
	blockCount uint64,
	lastBlock rpc.BlockNumber,
	rewardPercentiles []float64,
) (firstBlock *big.Int, reward [][]*big.Int, baseFee []*big.Int, gasUsedRatio []float64, baseFeePerBlobGas []*big.Int, blobGasUsedRatio []float64, err error) {
	return b.gpo.FeeHistory(ctx, blockCount, lastBlock, rewardPercentiles)
}

func (b *EthAPIBackend) BlobBaseFee(ctx context.Context) *big.Int {
	if excess := b.CurrentHeader().ExcessBlobGas; excess != nil {
		return eip4844.CalcBlobFee(b.ChainConfig(), b.CurrentHeader())
	}
	return nil
}

func (b *EthAPIBackend) ChainDb() ethdb.Database {
	return b.eth.ChainDb()
}

func (b *EthAPIBackend) AccountManager() *accounts.Manager {
	return b.eth.AccountManager()
}

func (b *EthAPIBackend) ExtRPCEnabled() bool {
	return b.extRPCEnabled
}

func (b *EthAPIBackend) UnprotectedAllowed() bool {
	return b.allowUnprotectedTxs
}

func (b *EthAPIBackend) RPCGasCap() uint64 {
	return b.eth.config.RPCGasCap
}

func (b *EthAPIBackend) RPCEVMTimeout() time.Duration {
	return b.eth.config.RPCEVMTimeout
}

func (b *EthAPIBackend) RPCTxFeeCap() float64 {
	return b.eth.config.RPCTxFeeCap
}

func (b *EthAPIBackend) CurrentView() *filtermaps.ChainView {
	head := b.eth.blockchain.CurrentBlock()
	if head == nil {
		return nil
	}
	return filtermaps.NewChainView(b.eth.blockchain, head.Number.Uint64(), head.Hash())
}

func (b *EthAPIBackend) NewMatcherBackend() filtermaps.MatcherBackend {
	return b.eth.filterMaps.NewMatcherBackend()
}

func (b *EthAPIBackend) Engine() consensus.Engine {
	return b.eth.engine
}

func (b *EthAPIBackend) CurrentHeader() *types.Header {
	return b.eth.blockchain.CurrentHeader()
}

func (b *EthAPIBackend) StateAtBlock(
	ctx context.Context,
	block *types.Block,
	reexec uint64,
	base *state.StateDB,
	readOnly bool,
	preferDisk bool,
) (*state.StateDB, tracers.StateReleaseFunc, error) {
	return b.eth.stateAtBlock(ctx, block, reexec, base, readOnly, preferDisk)
}

func (b *EthAPIBackend) StateAtTransaction(
	ctx context.Context,
	block *types.Block,
	txIndex int,
	reexec uint64,
) (*types.Transaction, vm.BlockContext, *state.StateDB, tracers.StateReleaseFunc, error) {
	return b.eth.stateAtTransaction(ctx, block, txIndex, reexec)
}

func (b *EthAPIBackend) HistoricalRPCService() *rpc.Client {
	return b.eth.historicalRPCService
}

func (b *EthAPIBackend) Genesis() *types.Block {
	return b.eth.blockchain.Genesis()
}

// HandleSPMessage processes messages received from the shared publisher.
// SSV
func (b *EthAPIBackend) HandleSPMessage(ctx context.Context, msg *rollupv1.Message) ([]common.Hash, error) {
	if b.coordinator == nil {
		return nil, fmt.Errorf("coordinator not configured")
	}

	// If this call originates from local RPC (SendXTransaction) we set ctx value "forward".
	// Forward XTRequest to the SP over transport instead of handling locally.
	if forward, _ := ctx.Value("forward").(bool); forward {
		switch msg.Payload.(type) {
		case *rollupv1.Message_XtRequest:
			if b.spClient == nil {
				return nil, fmt.Errorf("shared publisher client not configured")
			}
			if msg.SenderId == "" {
				msg.SenderId = b.ChainConfig().ChainID.String()
			}
			if err := b.spClient.Send(ctx, msg); err != nil {
				return nil, fmt.Errorf("failed to forward XTRequest to shared publisher: %w", err)
			}
			return nil, nil
		}
	}

	// Default path: route inbound messages (from SP or peers) to the SBCP coordinator
	if err := b.coordinator.HandleMessage(ctx, msg.SenderId, msg); err != nil {
		return nil, fmt.Errorf("coordinator failed to handle %T: %w", msg.Payload, err)
	}
	return nil, nil
}

func (b *EthAPIBackend) isCoordinator(ctx context.Context, mailboxProcessor *MailboxProcessor) error {
	chainID := b.ChainConfig().ChainID.Uint64()
	mailboxAddr := b.GetMailboxAddressFromChainID(chainID)

	// Fetch the full block for the current head before creating a state view
	head := b.eth.blockchain.CurrentBlock()
	if head == nil {
		return fmt.Errorf("current head not available")
	}
	block := b.eth.blockchain.GetBlock(head.Hash(), head.Number.Uint64())
	if block == nil {
		return fmt.Errorf("failed to retrieve current block %s", head.Hash())
	}

	stateDB, release, err := b.StateAtBlock(ctx, block, 0, nil, false, false)
	if err != nil {
		return err
	}
	defer release()

	mailboxCode := stateDB.GetCode(mailboxAddr)
	if len(mailboxCode) == 0 {
		return fmt.Errorf("mailbox code not found at address %s for %d chain id", mailboxAddr.String(), chainID)
	}

	coordinatorAddr, err := mailboxProcessor.getCoordinatorAddress(ctx, mailboxAddr)
	if err != nil {
		return err
	}

	if coordinatorAddr != b.sequencerAddress {
		return fmt.Errorf(
			"sequencer is not coordinator, coordinatorAddr: %s, sequencerAddr: %s",
			coordinatorAddr.Hex(),
			b.sequencerAddress.Hex(),
		)
	}

	return nil
}

func successfulAll(coordinationStates []*SimulationState) bool {
	for _, s := range coordinationStates {
		// checking if any transaction reverted or requires processing CIRCMessage
		if !s.Success || len(s.Dependencies) > 0 {
			return false
		}
	}

	return true
}

func requiresCoordination(coordinationStates []*SimulationState) bool {
	for _, s := range coordinationStates {
		if s.RequiresCoordination() {
			return true
		}
	}

	return false
}

// handleSequencerMessage processes messages received from sequencer clients (peer-to-peer).
// SSV
func (b *EthAPIBackend) handleSequencerMessage(
	ctx context.Context,
	chainID string,
	msg *rollupv1.Message,
) ([]common.Hash, error) {
	log.Debug(
		"[SSV] Handling message from sequencer",
		"chainID",
		chainID,
		"senderID",
		msg.SenderId,
		"type",
		fmt.Sprintf("%T", msg.Payload),
	)

	// Handle CrossChainTxResult messages locally (update tracker)
	if txResult, ok := msg.Payload.(*rollupv1.Message_CrossChainTxResult); ok {
		return b.handleCrossChainTxResult(ctx, txResult.CrossChainTxResult)
	}

	// All other messages go to the coordinator
	if b.coordinator == nil {
		return nil, fmt.Errorf("coordinator not configured for sequencer message from chainID %s", chainID)
	}

	if err := b.coordinator.HandleMessage(ctx, msg.SenderId, msg); err != nil {
		log.Error("[SSV] Failed to handle message from sequencer", "chainID", chainID, "err", err)
		return nil, fmt.Errorf("coordinator failed to handle %T from sequencer %s: %w", msg.Payload, chainID, err)
	}

	return nil, nil
}

// handleCrossChainTxResult processes CrossChainTxResult messages to update the local tracker
// with hash mappings from remote chains that re-signed transactions.
// SSV
func (b *EthAPIBackend) handleCrossChainTxResult(ctx context.Context, result *rollupv1.CrossChainTxResult) ([]common.Hash, error) {
	if b.xtTracker == nil {
		return nil, fmt.Errorf("XT tracker not configured")
	}

	remoteChainID := new(big.Int).SetBytes(result.ChainId)
	originalHash := common.BytesToHash(result.OriginalHash)
	finalHash := common.BytesToHash(result.FinalHash)

	log.Info("[SSV] Received CrossChainTxResult from remote chain",
		"xtID", result.XtId.Hex(),
		"remoteChainID", remoteChainID.String(),
		"originalHash", originalHash.Hex(),
		"finalHash", finalHash.Hex())

	// Update our local tracker with the hash mapping
	// The tracker should map originalHash -> finalHash for this remote chain
	b.xtTracker.UpdateHashMapping(result.XtId, remoteChainID.String(), originalHash, finalHash)

	return nil, nil
}

// StartCallbackFn returns a function that can be used to send transaction bundles to the shared publisher.
// SSV
func (b *EthAPIBackend) StartCallbackFn(chainID *big.Int) spconsensus.StartFn {
	_ = chainID
	return func(ctx context.Context, from string, xtReq *rollupv1.XTRequest) error {
		log.Warn("[SSV] Suppressing StartCallback XTRequest forward (SBCP-only)", "from", from)
		return nil
	}
}

// CIRCCallbackFn returns a function that triggers re-simulation when CIRC messages are received.
// SSV
func (b *EthAPIBackend) CIRCCallbackFn(chainID *big.Int) spconsensus.CIRCFn {
	return func(ctx context.Context, xtID *rollupv1.XtID, circMessage *rollupv1.CIRCMessage) error {
		log.Info("[SSV] CIRC message received via callback, triggering re-simulation for ACK detection",
			"xtID", xtID.Hex(),
			"sourceChain", new(big.Int).SetBytes(circMessage.SourceChain).String())

		log.Info("[SSV] CIRC callback completed - main simulation loop will detect new messages", "xtID", xtID.Hex())

		return nil
	}
}

// VoteCallbackFn returns a function that can be used to send votes for cross-chain transactions.
// SSV
func (b *EthAPIBackend) VoteCallbackFn(chainID *big.Int) spconsensus.VoteFn {
	return func(ctx context.Context, xtID *rollupv1.XtID, vote bool) error {
		msgVote := &rollupv1.Message_Vote{
			Vote: &rollupv1.Vote{
				Vote:          vote,
				XtId:          xtID,
				SenderChainId: chainID.Bytes(),
			},
		}

		spMsg := &rollupv1.Message{
			SenderId: chainID.String(),
			Payload:  msgVote,
		}
		return b.spClient.Send(ctx, spMsg)
	}
}

func (b *EthAPIBackend) SimulateTransaction(
	ctx context.Context,
	tx *types.Transaction,
	blockNrOrHash rpc.BlockNumberOrHash,
) (*ssv.SSVTraceResult, error) {
	timer := time.Now()
	defer func() {
		log.Info("[SSV] Simulated transaction with SSV trace", "txHash", tx.Hash().Hex(), "duration", time.Since(timer))
	}()

	ctx = context.WithValue(ctx, "simulation", true)

	// stateDB should have clear() and putInbox() in its state
	stateDB, header, err := b.StateAndHeaderByNumberOrHash(ctx, blockNrOrHash)
	if err != nil {
		return nil, err
	}

	stateDB.Finalise(true)
	snapshot := stateDB.Snapshot()
	defer stateDB.RevertToSnapshot(snapshot)

	signer := types.MakeSigner(b.ChainConfig(), header.Number, header.Time)
	msg, err := core.TransactionToMessage(tx, signer, header.BaseFee)
	if err != nil {
		return nil, err
	}

	// If this transaction has a higher nonce than current state nonce,
	// it means nonce space was reserved for putInbox transactions that will be created
	// later. We need to advance the state nonce to match the transaction's nonce so
	// simulation doesn't fail.
	currentNonce := stateDB.GetNonce(msg.From)
	if tx.Nonce() > currentNonce {
		// Set nonce to match the transaction, effectively "consuming" the reserved nonces
		stateDB.SetNonce(msg.From, tx.Nonce(), tracing.NonceChangeUnspecified)
		log.Info("[SSV] Advanced state nonce for simulation",
			"from", msg.From.Hex(),
			"oldNonce", currentNonce,
			"newNonce", tx.Nonce(),
			"txHash", tx.Hash().Hex())
	}

	mailboxAddresses := b.GetMailboxAddresses()
	tracer := native.NewSSVTracer(mailboxAddresses)

	vmConfig := vm.Config{}
	if b.eth.blockchain.GetVMConfig() != nil {
		vmConfig = *b.eth.blockchain.GetVMConfig()
	}
	vmConfig.Tracer = tracer.Hooks()
	vmConfig.EnablePreimageRecording = true

	blockContext := core.NewEVMBlockContext(header, b.eth.blockchain, nil, b.ChainConfig(), stateDB)

	evm := vm.NewEVM(blockContext, stateDB, b.ChainConfig(), vmConfig)

	if ctx.Value("simulation") != nil {
		for _, staged := range b.GetPendingPutInboxTxs() {
			if staged == nil || staged.Hash() == tx.Hash() {
				continue
			}

			stageMsg, err := core.TransactionToMessage(staged, signer, header.BaseFee)
			if err != nil {
				log.Warn("[SSV] Failed to build staged putInbox message", "txHash", staged.Hash(), "err", err)
				continue
			}

			stageGasPool := new(core.GasPool).AddGas(header.GasLimit)
			stateDB.SetTxContext(staged.Hash(), stateDB.TxIndex()+1)

			// Check if we need to skip nonces to reach the putInbox transaction's nonce
			currentNonce := stateDB.GetNonce(stageMsg.From)
			wantNonce := staged.Nonce()

			log.Info("[SSV] Pre-applying putInbox transaction",
				"txHash", staged.Hash().Hex(),
				"currentNonce", currentNonce,
				"wantNonce", wantNonce)

			// Only apply if nonces match - don't manually set nonces
			if currentNonce != wantNonce {
				log.Warn("[SSV] Skipping putInbox pre-apply due to nonce mismatch",
					"txHash", staged.Hash().Hex(),
					"currentNonce", currentNonce,
					"wantNonce", wantNonce)
				continue
			}

			if _, err := core.ApplyMessage(evm, stageMsg, stageGasPool); err != nil {
				log.Warn("[SSV] Failed to pre-apply putInbox transaction", "txHash", staged.Hash(), "err", err)
				continue
			}
			log.Info("[SSV] Pre-applied putInbox for simulation",
				"stagedHash", staged.Hash().Hex(),
			)
			stateDB.Finalise(true)
		}
	}

	stateDB.SetTxContext(tx.Hash(), stateDB.TxIndex()+1)

	gasPool := new(core.GasPool).AddGas(header.GasLimit)
	result, err := core.ApplyMessage(evm, msg, gasPool)
	if err != nil {
		log.Error("[SSV] EVM execution failed during simulation - REASON: evm_apply_message_error",
			"txHash", tx.Hash().Hex(),
			"error", err,
			"failure_reason", "evm_apply_message_error")
		return nil, err
	}

	traceResult := tracer.GetTraceResult()
	traceResult.ExecutionResult = result

	return traceResult, nil
}

// SubmitSequencerTransaction submits a transaction with a priority flag.
// SSV
func (b *EthAPIBackend) SubmitSequencerTransaction(ctx context.Context, tx *types.Transaction, isPutInbox bool) error {
	if err := b.validateSequencerTransaction(tx); err != nil {
		log.Error("[SSV] Sequencer transaction validation failed", "err", err, "txHash", tx.Hash().Hex())
		return fmt.Errorf("sequencer transaction validation failed: %w", err)
	}

	if isPutInbox {
		b.AddPendingPutInboxTx(tx)
	}
	// Also inject into the local txpool so that PENDING state reflects these txs
	// and re-simulation against rpc.PendingBlockNumber can observe mailbox effects.
	if err := b.sendTx(ctx, tx); err != nil {
		log.Warn(
			"[SSV] Failed to inject sequencer tx into txpool (continuing with staged include)",
			"err",
			err,
			"txHash",
			tx.Hash().Hex(),
		)
	}
	return nil
}

// ConfigureMailboxes sets the mailbox contract addresses for known rollups.
// SSV
func (b *EthAPIBackend) ConfigureMailboxes(raw map[uint64]string) error {
	ordered := []uint64{native.RollupAChainID, native.RollupBChainID}
	addresses := make([]common.Address, 0, len(ordered))
	mailboxMap := make(map[uint64]common.Address, len(ordered))

	for _, chainID := range ordered {
		addrStr := strings.TrimSpace(raw[chainID])
		if addrStr == "" {
			mailboxMap[chainID] = common.Address{}
			addresses = append(addresses, common.Address{})
			log.Warn("[SSV] Mailbox address not configured", "chainID", chainID)
			continue
		}
		if !common.IsHexAddress(addrStr) {
			return fmt.Errorf("invalid mailbox address %q for chain %d", addrStr, chainID)
		}
		addr := common.HexToAddress(addrStr)
		mailboxMap[chainID] = addr
		addresses = append(addresses, addr)
	}

	b.mailboxAddresses = addresses
	b.mailboxByChainID = mailboxMap
	native.ReplaceChainIDToMailbox(mailboxMap)
	return nil
}

// GetMailboxAddresses returns the list of mailbox contract addresses to watch.
// SSV
func (b *EthAPIBackend) GetMailboxAddresses() []common.Address {
	if len(b.mailboxAddresses) == 0 {
		return nil
	}
	out := make([]common.Address, len(b.mailboxAddresses))
	copy(out, b.mailboxAddresses)
	return out
}

func (b *EthAPIBackend) GetMailboxAddressFromChainID(chainID uint64) common.Address {
	if b.mailboxByChainID == nil {
		return common.Address{}
	}
	return b.mailboxByChainID[chainID]
}

// AddPendingPutInboxTx adds a putInbox transaction to the pending list.
// SSV
func (b *EthAPIBackend) AddPendingPutInboxTx(tx *types.Transaction) {
	b.sequencerTxMutex.Lock()
	defer b.sequencerTxMutex.Unlock()

	b.pendingPutInboxTxs = append(b.pendingPutInboxTxs, tx)

	log.Info("[SSV] Added putInbox transaction to mempool",
		"txHash", tx.Hash().Hex(),
		"totalPending", len(b.pendingPutInboxTxs),
		"nonce", tx.Nonce(),
	)
}

// GetPendingPutInboxTxs returns all pending putInbox transactions.
// SSV
func (b *EthAPIBackend) GetPendingPutInboxTxs() []*types.Transaction {
	b.sequencerTxMutex.RLock()
	defer b.sequencerTxMutex.RUnlock()

	result := make([]*types.Transaction, len(b.pendingPutInboxTxs))
	copy(result, b.pendingPutInboxTxs)
	return result
}

// ClearSequencerTransactionsAfterBlock clears all pending sequencer transactions after block creation
// SSV
func (b *EthAPIBackend) ClearSequencerTransactionsAfterBlock() {
	if b.coordinator == nil {
		log.Info("[SSV] Clearing transactions - non-SBCP mode")
		b.clearAllSequencerTransactions()
		return
	}

	currentState := b.coordinator.GetState()
	slot := b.coordinator.GetCurrentSlot()

	log.Info("[SSV] Transaction clearing request",
		"state", currentState.String(),
		"slot", slot,
		"pending", len(b.GetPendingPutInboxTxs())+len(b.GetPendingOriginalTxs()))

	switch currentState {
	case sequencer.StateBuildingFree, sequencer.StateBuildingLocked:
		log.Info("[SSV] Preserving transactions during coordination")
		return
	default:
		log.Info("[SSV] Clearing transactions")
		b.clearAllSequencerTransactions()
	}
}

// clearAllSequencerTransactions performs the actual clearing of transactions
// SSV
func (b *EthAPIBackend) clearAllSequencerTransactions() {
	b.sequencerTxMutex.Lock()
	defer b.sequencerTxMutex.Unlock()

	putInboxCount := len(b.pendingPutInboxTxs)
	originalCount := len(b.pendingSequencerTxs)

	b.pendingPutInboxTxs = nil
	b.pendingSequencerTxs = nil

	log.Info("[SSV] Cleared sequencer transactions",
		"putInbox", putInboxCount,
		"original", originalCount)
}

// SubscribeXTResult registers a waiter for the result of a forwarded XT request.
func (b *EthAPIBackend) SubscribeXTResult(xtID *rollupv1.XtID) (<-chan xt.XTResult, func(), error) {
	return b.xtTracker.Subscribe(xtID)
}

// PrepareSequencerTransactionsForBlock prepares sequencer transactions for inclusion in a new block
// SSV
func (b *EthAPIBackend) PrepareSequencerTransactionsForBlock(ctx context.Context) error {
	if b.coordinator == nil {
		log.Info("[SSV] Preparing sequencer transactions", "state", "non-SBCP")
		return b.prepareAllCrossChainTransactionsForSubmission(ctx)
	}

	currentState := b.coordinator.GetState()
	currentSlot := b.coordinator.GetCurrentSlot()

	log.Info("[SSV] Preparing sequencer transactions",
		"state", currentState.String(),
		"slot", currentSlot,
		"putInbox", len(b.GetPendingPutInboxTxs()),
		"original", len(b.GetPendingOriginalTxs()))

	switch currentState {
	case sequencer.StateBuildingFree, sequencer.StateBuildingLocked:
		log.Info("[SSV] Coordination state - excluding cross-chain txs from block")
		if err := b.coordinator.PrepareTransactionsForBlock(ctx, currentSlot); err != nil {
			log.Warn("[SSV] Coordinator failed to prepare transactions", "err", err)
		}
		return nil
	case sequencer.StateSubmission:
		log.Info("[SSV] Submission state - preparing ALL cross-chain txs")
		return b.prepareAllCrossChainTransactionsForSubmission(ctx)
	default:
		return nil
	}
}

// prepareAllCrossChainTransactionsForSubmission prepares all cross-chain transactions for inclusion
// SSV
func (b *EthAPIBackend) prepareAllCrossChainTransactionsForSubmission(ctx context.Context) error {
	return nil
}

// GetOrderedTransactionsForBlock returns only sequencer-managed transactions in
// the correct order for block inclusion. Normal mempool transactions are
// included by the miner after this list, and must not be returned here.
// SSV
func (b *EthAPIBackend) GetOrderedTransactionsForBlock(
	ctx context.Context,
) (types.Transactions, error) {
	if b.coordinator == nil {
		// Non-SBCP mode: return sequencer-managed txs only; miner appends normals
		return b.buildSequencerOnlyList(), nil
	}

	currentState := b.coordinator.GetState()
	slot := b.coordinator.GetCurrentSlot()

	log.Info("[SSV] Building sequencer transaction list",
		"state", currentState.String(),
		"slot", slot)

	switch currentState {
	case sequencer.StateBuildingFree, sequencer.StateBuildingLocked:
		log.Info("[SSV] Coordination block - no sequencer txs to include")
		return types.Transactions{}, nil
	case sequencer.StateSubmission:
		log.Info("[SSV] Submission block - sequencer-managed txs first")
		return b.buildSequencerOnlyList(), nil
	default:
		log.Info("[SSV] Default block - no sequencer txs to include")
		return types.Transactions{}, nil
	}
}

// buildSequencerOnlyList assembles only the sequencer-managed transactions in the
// correct internal order: putInbox(), then the original user txs.
// Normal mempool transactions are not part of this list.
func (b *EthAPIBackend) buildSequencerOnlyList() types.Transactions {
	var orderedTxs types.Transactions

	for _, tx := range b.GetPendingPutInboxTxs() {
		orderedTxs = append(orderedTxs, tx)
	}
	for _, tx := range b.GetPendingOriginalTxs() {
		orderedTxs = append(orderedTxs, tx)
	}

	log.Info("[SSV] Built sequencer-only tx list",
		"putInbox", len(b.GetPendingPutInboxTxs()),
		"original", len(b.GetPendingOriginalTxs()),
		"total", len(orderedTxs),
	)
	return orderedTxs
}

// buildFullCrossChainBlock builds the final block with all cross-chain transactions
// SSV
func (b *EthAPIBackend) buildFullCrossChainBlock(
	ctx context.Context,
	normalTxs types.Transactions,
) (types.Transactions, error) {
	var orderedTxs types.Transactions

	putInboxTxs := b.GetPendingPutInboxTxs()
	if len(putInboxTxs) > 0 {
		orderedTxs = append(orderedTxs, putInboxTxs...)
	}

	originalTxs := b.GetPendingOriginalTxs()
	if len(originalTxs) > 0 {
		orderedTxs = append(orderedTxs, originalTxs...)
	}

	filteredNormalTxs := b.filterOutSequencerTransactions(normalTxs)
	orderedTxs = append(orderedTxs, filteredNormalTxs...)

	log.Info("[SSV] Built cross-chain block",
		"putInbox", len(putInboxTxs),
		"original", len(originalTxs),
		"normal", len(filteredNormalTxs),
		"total", len(orderedTxs))

	return orderedTxs, nil
}

// filterOutSequencerTransactions removes sequencer transactions from normal transaction list
// SSV
func (b *EthAPIBackend) filterOutSequencerTransactions(txs types.Transactions) types.Transactions {
	var filtered types.Transactions
	sequencerTxHashes := make(map[common.Hash]bool)

	// Build map of sequencer transaction hashes
	for _, putInboxTx := range b.GetPendingPutInboxTxs() {
		sequencerTxHashes[putInboxTx.Hash()] = true
	}

	// Filter out sequencer transactions
	for _, tx := range txs {
		if !sequencerTxHashes[tx.Hash()] {
			filtered = append(filtered, tx)
		}
	}

	if len(filtered) != len(txs) {
		log.Debug("[SSV] Filtered out sequencer transactions",
			"original", len(txs),
			"filtered", len(filtered))
	}

	return filtered
}

// validateSequencerTransaction validates that a sequencer transaction is properly formed
// SSV
func (b *EthAPIBackend) validateSequencerTransaction(tx *types.Transaction) error {
	// Basic validation
	if tx == nil {
		return fmt.Errorf("transaction is nil")
	}

	if tx.To() == nil {
		return fmt.Errorf("sequencer transaction must have a destination")
	}

	// Check if it's targeting a mailbox address
	mailboxAddrs := b.GetMailboxAddresses()
	isMailboxTx := false
	for _, addr := range mailboxAddrs {
		if *tx.To() == addr {
			isMailboxTx = true
			break
		}
	}

	if !isMailboxTx {
		log.Warn("[SSV] Sequencer transaction not targeting mailbox",
			"to", tx.To().Hex(),
			"expected", mailboxAddrs)
	}

	// Validate gas limits
	if tx.Gas() < 21000 { // TODO: update this
		return fmt.Errorf("sequencer transaction gas too low: %d", tx.Gas())
	}

	if tx.Gas() > 1000000 { // TODO: update this
		log.Warn("[SSV] Sequencer transaction has high gas limit", "gas", tx.Gas())
	}

	log.Debug("[SSV] Sequencer transaction validated",
		"txHash", tx.Hash().Hex(),
		"to", tx.To().Hex(),
		"gas", tx.Gas(),
		"gasPrice", tx.GasPrice())

	return nil
}

// OnBlockBuildingStart is called when block building starts
// SSV
func (b *EthAPIBackend) OnBlockBuildingStart(context.Context) error {
	putInbox := b.GetPendingPutInboxTxs()
	original := b.GetPendingOriginalTxs()

	log.Info("[SSV] Block building started - preparing sequencer state",
		"state", func() string {
			if b.coordinator != nil {
				return b.coordinator.GetState().String()
			}
			return "unknown"
		}(),
		"slot", func() uint64 {
			if b.coordinator != nil {
				return b.coordinator.GetCurrentSlot()
			}
			return 0
		}(),
		"putInbox_count", len(putInbox),
		"original_count", len(original))

	if len(putInbox) > 0 {
		tx := putInbox[0]
		log.Info("[SSV] Pending putInbox", "index", 0, "txHash", tx.Hash().Hex(), "nonce", tx.Nonce())
	}
	if len(original) > 0 {
		tx := original[0]
		log.Info("[SSV] Pending original", "index", 0, "txHash", tx.Hash().Hex(), "nonce", tx.Nonce())
	}

	if b.coordinator != nil {
		_ = b.coordinator.OnBlockBuildingStart(context.Background(), b.coordinator.GetCurrentSlot())
	}

	return nil
}

// OnBlockBuildingComplete is called when block building completes
// SSV
func (b *EthAPIBackend) OnBlockBuildingComplete(
	ctx context.Context,
	block *types.Block,
	success, simulation bool,
) error {
	if !success || block == nil || simulation {
		return nil
	}

	if b.coordinator == nil {
		log.Info("[SSV] Block completed - non-SBCP mode", "blockNumber", block.NumberU64())
		return b.handleSubmissionBlock(ctx, block, 0)
	}

	currentState := b.coordinator.GetState()
	slot := b.coordinator.GetCurrentSlot()

	log.Info("[SSV] Block building completed",
		"state", currentState.String(),
		"slot", slot,
		"blockNumber", block.NumberU64(),
		"txs", len(block.Transactions()))

	switch currentState {
	case sequencer.StateBuildingFree, sequencer.StateBuildingLocked:
		log.Info("[SSV] Coordination block completed - not storing")
		return nil
	case sequencer.StateSubmission:
		log.Info("[SSV] Submission block completed - storing and sending")
		return b.handleSubmissionBlock(ctx, block, slot)
	default:
		log.Info("[SSV] Default block completed")
		return nil
	}
}

// handleSubmissionBlock handles the final block containing all cross-chain transactions
// SSV
func (b *EthAPIBackend) handleSubmissionBlock(ctx context.Context, block *types.Block, slot uint64) error {
	b.pendingBlockMutex.Lock()
	defer b.pendingBlockMutex.Unlock()

	if b.pendingBlock != nil && b.pendingBlockSlot == slot {
		log.Debug("[SSV] Submission block already stored", "slot", slot)
		return nil
	}

	if b.coordinator != nil && b.coordinator.GetActiveSCPInstanceCount() > 0 {
		log.Info("[SSV] Delaying submission block - active SCP instances",
			"activeSCP", b.coordinator.GetActiveSCPInstanceCount())
		return nil
	}

	log.Info("[SSV] Storing submission block",
		"slot", slot,
		"blockNumber", block.NumberU64(),
		"txs", len(block.Transactions()))

	b.pendingBlock = block
	b.pendingBlockSlot = slot

	if b.coordinator != nil && b.coordinator.GetState() == sequencer.StateSubmission {
		log.Info("[SSV] Sending submission block to SP", "slot", slot)
		go func() {
			if err := b.sendStoredL2Block(context.Background()); err != nil {
				log.Error("[SSV] Failed to send submission block", "err", err, "slot", slot)
			} else {
				log.Info("[SSV] Submission block sent successfully", "slot", slot)
				b.clearAllSequencerTransactions()
			}
		}()
	}

	return nil
}

func (b *EthAPIBackend) GetPendingOriginalTxs() []*types.Transaction {
	b.sequencerTxMutex.RLock()
	defer b.sequencerTxMutex.RUnlock()

	result := make([]*types.Transaction, len(b.pendingSequencerTxs))
	copy(result, b.pendingSequencerTxs)

	return result
}

// reSimulateAfterMailboxPopulation re-simulates transactions after mailbox has been populated
// SSV
func (b *EthAPIBackend) reSimulateAfterMailboxPopulation(
	ctx context.Context,
	xtReq *rollupv1.XTRequest,
	xtID *rollupv1.XtID,
	coordinationStates []*SimulationState,
) (bool, error) {
	chainID := b.ChainConfig().ChainID

	log.Info("[SSV] Starting re-simulation after mailbox population",
		"xtID", xtID.Hex(),
		"chainID", chainID,
		"transactions", len(xtReq.Transactions))

	// Wait for putInbox transactions to be processed
	if err := b.waitForPutInboxTransactionsToBeProcessed(); err != nil {
		log.Error("[SSV] Failed waiting for putInbox transactions", "error", err, "xtID", xtID.Hex())
		return false, err
	}

	// Re-simulate each local transaction against PENDING state (so the view
	// includes just-created putInbox transactions not yet part of latest).
	allSuccessful := true
	blockNrOrHash := rpc.BlockNumberOrHashWithNumber(rpc.PendingBlockNumber)

	for _, txReq := range xtReq.Transactions {
		txChainID := new(big.Int).SetBytes(txReq.ChainId)

		// Only re-simulate transactions for our local chain
		if txChainID.Cmp(chainID) != 0 {
			continue
		}

		log.Info("[SSV] Re-simulating local transactions against pending state",
			"chainID", txChainID,
			"txCount", len(txReq.Transaction))

		for i, txBytes := range txReq.Transaction {
			tx := new(types.Transaction)
			if err := tx.UnmarshalBinary(txBytes); err != nil {
				log.Error("[SSV] Failed to unmarshal transaction for re-simulation - REASON: transaction_unmarshal_failed",
					"error", err,
					"index", i,
					"xtID", xtID.Hex(),
					"failure_reason", "transaction_unmarshal_failed")
				allSuccessful = false
				continue
			}

			// Re-simulate the transaction
			success, err := b.reSimulateTransaction(ctx, tx, blockNrOrHash, xtID)
			if err != nil {
				log.Error("[SSV] Re-simulation error - REASON: simulation_error",
					"txHash", tx.Hash().Hex(),
					"error", err,
					"xtID", xtID.Hex(),
					"failure_reason", "simulation_error")
				allSuccessful = false
				continue
			}

			if !success {
				log.Warn("[SSV] Re-simulation failed for transaction - REASON: see transaction-specific logs above",
					"txHash", tx.Hash().Hex(),
					"xtID", xtID.Hex(),
					"failure_reason", "simulation_returned_false")
				allSuccessful = false
			} else {
				log.Info("[SSV] Re-simulation successful for transaction",
					"txHash", tx.Hash().Hex(),
					"xtID", xtID.Hex())
			}
		}
	}

	log.Info("[SSV] Re-simulation completed",
		"xtID", xtID.Hex(),
		"allSuccessful", allSuccessful)

	return allSuccessful, nil
}

// reSimulateTransaction re-simulates a single transaction and checks for success
// SSV
func (b *EthAPIBackend) reSimulateTransaction(
	ctx context.Context,
	tx *types.Transaction,
	blockNrOrHash rpc.BlockNumberOrHash,
	xtID *rollupv1.XtID,
) (bool, error) {
	log.Debug("[SSV] Re-simulating transaction",
		"txHash", tx.Hash().Hex(),
		"xtID", xtID.Hex())

	// Simulate with SSV tracing to detect mailbox interactions
	traceResult, err := b.SimulateTransaction(ctx, tx, blockNrOrHash)
	if err != nil {
		log.Error("[SSV] Transaction simulation with trace failed - REASON: simulation_trace_error",
			"txHash", tx.Hash().Hex(),
			"error", err,
			"xtID", xtID.Hex(),
			"failure_reason", "simulation_trace_error")
		return false, err
	}

	// Check if execution was successful
	if traceResult.ExecutionResult.Err != nil {
		log.Warn("[SSV] Transaction execution failed in re-simulation - REASON: execution_error",
			"txHash", tx.Hash().Hex(),
			"executionError", traceResult.ExecutionResult.Err,
			"xtID", xtID.Hex(),
			"failure_reason", "execution_error")
		return false, nil
	}

	// Validate that the transaction used reasonable gas (not failed silently)
	if traceResult.ExecutionResult.UsedGas == 0 {
		log.Warn("[SSV] Transaction used no gas, likely failed silently - REASON: zero_gas_used",
			"txHash", tx.Hash().Hex(),
			"xtID", xtID.Hex(),
			"failure_reason", "zero_gas_used")
		return false, nil
	}

	// Check that mailbox operations were traced (indicating they succeeded)
	if len(traceResult.Operations) == 0 {
		log.Warn("[SSV] No mailbox operations detected in re-simulation - REASON: no_mailbox_operations",
			"txHash", tx.Hash().Hex(),
			"xtID", xtID.Hex(),
			"failure_reason", "no_mailbox_operations")
		return false, nil
	}

	log.Debug("[SSV] Transaction re-simulation successful",
		"txHash", tx.Hash().Hex(),
		"gasUsed", traceResult.ExecutionResult.UsedGas,
		"mailboxOps", len(traceResult.Operations),
		"xtID", xtID.Hex())

	return true, nil
}

// waitForPutInboxTransactionsToBeProcessed waits for putInbox transactions to be included
// SSV
func (b *EthAPIBackend) waitForPutInboxTransactionsToBeProcessed() error {
	putInboxTxs := b.GetPendingPutInboxTxs()
	if len(putInboxTxs) == 0 {
		return nil
	}

	// Wait for transactions to be in txpool
	for _, tx := range putInboxTxs {
		timeout := time.After(5 * time.Second)
		ticker := time.NewTicker(100 * time.Millisecond)

		func() {
			defer ticker.Stop() // Now properly scoped to this transaction
			for {
				select {
				case <-timeout:
					log.Error("timed out waiting for putInbox transaction appearance in pool")
					return // This will trigger the defer and stop the ticker
				case <-ticker.C:
					if poolTx := b.GetPoolTransaction(tx.Hash()); poolTx != nil {
						log.Info("[SSV] found putInbox transaction in pool", "hash", tx.Hash().Hex())
						// small settling delay so miner refresh picks it up in pending view
						time.Sleep(450 * time.Millisecond)
						return // This will trigger the defer and stop the ticker
					}
				}
			}
		}()
	}

	return nil
}

func (b *EthAPIBackend) poolPayloadTx(tx *types.Transaction) {
	b.sequencerTxMutex.Lock()
	defer b.sequencerTxMutex.Unlock()

	b.pendingSequencerTxs = append(b.pendingSequencerTxs, tx)
}

// SetSequencerCoordinator wires an SBCP sequencer coordinator, consensus callbacks, and SP client routing.
// SSV
func (b *EthAPIBackend) SetSequencerCoordinator(coord sequencer.Coordinator, sp transport.Client) {
	b.coordinator = coord
	b.spClient = sp

	if b.spClient != nil {
		b.spClient.SetHandler(b.HandleSPMessage)
	}

	// Set handlers for sequencer clients to receive CIRC messages
	for chainID, client := range b.sequencerClients {
		if client != nil {
			// Capture chainID in closure to avoid loop variable issues
			chainID := chainID
			client.SetHandler(func(ctx context.Context, msg *rollupv1.Message) ([]common.Hash, error) {
				return b.handleSequencerMessage(ctx, chainID, msg)
			})

			log.Info("[SSV] Sequencer client handler set", "peerChainID", chainID)
		}
	}

	if b.coordinator != nil {
		// Wire consensus callbacks for SCP → coordinator integration
		if b.coordinator.Consensus() != nil {
			chainID := b.ChainConfig().ChainID
			b.coordinator.Consensus().SetStartCallback(b.StartCallbackFn(chainID))
			b.coordinator.Consensus().SetVoteCallback(b.VoteCallbackFn(chainID))
			b.coordinator.Consensus().SetCIRCCallback(b.CIRCCallbackFn(chainID))
		}

		// Register SBCP callbacks
		b.coordinator.SetCallbacks(sequencer.CoordinatorCallbacks{
			// For SBCP mode simulation during StartSC
			SimulateAndVote: b.simulateXTRequestForSBCP,

			OnVoteDecision: func(ctx context.Context, xtID *rollupv1.XtID, chainID string, vote bool) error {
				log.Debug("[SSV] Vote decision", "xtID", xtID.Hex(), "vote", vote)
				return nil
			},

			// Decisions are handled via consensus DecisionCallback above.
			OnFinalDecision: nil,

			OnBlockReady: func(ctx context.Context, block *rollupv1.L2Block, xtIDs []*rollupv1.XtID) error {
				log.Info("[SSV] SBCP block ready", "slot", block.Slot, "xtIDs", len(xtIDs))
				return nil
			},

			OnStateTransition: func(from, to sequencer.State, slot uint64, reason string) {
				log.Info("[SSV] SBCP state transition", "from", from.String(), "to", to.String(), "slot", slot)
				// Do not clear staged sequencer txs on generic Waiting transitions.
				// They are cleared explicitly in OnBlockBuildingComplete once we know
				// whether they were included.
			},
		})

		// Set miner notifier and start
		b.coordinator.SetMinerNotifier(b)
	}
}

// SSV
func (b *EthAPIBackend) NotifySlotStart(startSlot *rollupv1.StartSlot) error {
	log.Info("[SSV] Notify miner: StartSlot", "slot", startSlot.Slot, "next_sb", startSlot.NextSuperblockNumber)
	// Integrate with miner if needed (e.g., trigger prefetch/prep). Placeholder for now.
	return nil
}

// SSV
func (b *EthAPIBackend) NotifyRequestSeal(requestSeal *rollupv1.RequestSeal) error {
	log.Info("[SSV] Notify miner: RequestSeal", "slot", requestSeal.Slot, "included_xts", len(requestSeal.IncludedXts))

	// Store RequestSeal info first
	b.rsMutex.Lock()
	b.lastRequestSealIncluded = make([][]byte, len(requestSeal.IncludedXts))
	for i, xt := range requestSeal.IncludedXts {
		// copy to avoid aliasing
		dup := make([]byte, len(xt))
		copy(dup, xt)
		b.lastRequestSealIncluded[i] = dup
	}
	b.lastRequestSealSlot = requestSeal.Slot
	b.rsMutex.Unlock()

	// Send stored block if available (this fixes the "not accepting L2 blocks in state free" error)
	b.pendingBlockMutex.RLock()
	hasStoredBlock := b.pendingBlock != nil
	b.pendingBlockMutex.RUnlock()

	if hasStoredBlock {
		if err := b.sendStoredL2Block(context.Background()); err != nil {
			log.Error("[SSV] Failed to send stored L2Block after RequestSeal", "err", err, "slot", requestSeal.Slot)
		}
	} else {
		log.Info("[SSV] RequestSeal received with no stored block yet (will build now)", "slot", requestSeal.Slot)
	}

	return nil
}

// NotifyStateChange notifies the miner of sequencer state changes
// SSV
func (b *EthAPIBackend) NotifyStateChange(from, to sequencer.State, slot uint64) error {
	log.Debug("[SSV] SBCP state change", "from", from.String(), "to", to.String(), "slot", slot)
	return nil
}

// sendStoredL2Block sends the stored block as L2Block message
// SSV
func (b *EthAPIBackend) sendStoredL2Block(ctx context.Context) error {
	b.pendingBlockMutex.RLock()
	block := b.pendingBlock
	slot := b.pendingBlockSlot
	b.pendingBlockMutex.RUnlock()

	if block == nil {
		return fmt.Errorf("no stored block to send")
	}

	// Get RequestSeal inclusion list
	b.rsMutex.RLock()
	included := make([][]byte, len(b.lastRequestSealIncluded))
	for i := range b.lastRequestSealIncluded {
		dup := make([]byte, len(b.lastRequestSealIncluded[i]))
		copy(dup, b.lastRequestSealIncluded[i])
		included[i] = dup
	}
	b.rsMutex.RUnlock()

	// RLP encode the block
	var buf bytes.Buffer
	if err := block.EncodeRLP(&buf); err != nil {
		log.Error("[SSV] Failed to RLP encode block", "err", err, "blockHash", block.Hash().Hex())
		return err
	}

	l2 := &rollupv1.L2Block{
		Slot:            slot,
		ChainId:         b.ChainConfig().ChainID.Bytes(),
		BlockNumber:     block.NumberU64(),
		BlockHash:       block.Hash().Bytes(),
		ParentBlockHash: block.ParentHash().Bytes(),
		IncludedXts:     included,
		Block:           buf.Bytes(),
	}

	msg := &rollupv1.Message{
		SenderId: b.ChainConfig().ChainID.String(),
		Payload:  &rollupv1.Message_L2Block{L2Block: l2},
	}

	if err := b.spClient.Send(ctx, msg); err != nil {
		log.Error("[SSV] Failed to send L2Block to shared publisher", "err", err, "slot", slot)
		return err
	}

	log.Info("[SSV] Submitted L2Block to shared publisher",
		"slot", slot,
		"blockNumber", l2.BlockNumber,
		"blockHash", block.Hash().Hex(),
		"included_xts", len(included))

	// Mark included xTs as sent in consensus layer (SBCP path)
	if b.coordinator != nil && b.coordinator.Consensus() != nil {
		if err := b.coordinator.Consensus().OnL2BlockCommitted(ctx, l2); err != nil {
			log.Warn("[SSV] Consensus OnL2BlockCommitted warning", "err", err, "slot", slot)
		}
	}

	// Notify sequencer coordinator to complete the block lifecycle and
	// transition state back to Waiting.
	if b.coordinator != nil {
		if err := b.coordinator.OnBlockBuildingComplete(ctx, l2, true); err != nil {
			log.Warn("[SSV] Coordinator OnBlockBuildingComplete warning", "err", err, "slot", slot)
		}
	}

	// Clear stored block and reset RequestSeal state
	b.pendingBlockMutex.Lock()
	b.pendingBlock = nil
	b.pendingBlockSlot = 0
	b.pendingBlockMutex.Unlock()

	b.rsMutex.Lock()
	b.lastRequestSealIncluded = nil
	b.lastRequestSealSlot = 0
	b.rsMutex.Unlock()

	b.ClearSequencerTransactionsAfterBlock()
	log.Info("[SSV] Cleared sequencer transactions after successful L2Block submission")

	return nil
}

func (b *EthAPIBackend) simulateXTRequestForSBCP(
	ctx context.Context,
	xtReq *rollupv1.XTRequest,
	xtID *rollupv1.XtID,
) (allSuccessful bool, err error) {
	log.Info("[SSV] Simulating XT request for SBCP",
		"xtID", xtID.Hex(),
		"chainID", b.ChainConfig().ChainID,
		"txCount", len(xtReq.Transactions))

	var trackerNotified bool
	defer func() {
		if b.xtTracker == nil {
			return
		}
		if err != nil {
			b.xtTracker.PublishError(xtID, err)
			return
		}
		if !trackerNotified {
			b.xtTracker.Publish(xtID, nil)
		}
	}()

	chainID := b.ChainConfig().ChainID

	// Extract local transactions
	localTxs := make([]*rollupv1.TransactionRequest, 0)
	for _, txReq := range xtReq.Transactions {
		txChainID := new(big.Int).SetBytes(txReq.ChainId)
		if txChainID.Cmp(chainID) == 0 {
			localTxs = append(localTxs, txReq)
		}
	}

	if len(localTxs) == 0 {
		log.Info("[SSV] No local transactions to simulate", "xtID", xtID.Hex())
		return true, nil
	}

	sequencerAddr := crypto.PubkeyToAddress(b.sequencerKey.PublicKey)
	mailboxProcessor := NewMailboxProcessor(
		b.ChainConfig().ChainID.Uint64(),
		b.GetMailboxAddresses(),
		b.sequencerClients,
		b.coordinator,
		b.sequencerKey,
		sequencerAddr,
		b,
	)

	coordinationStates := make([]*SimulationState, 0)
	txDone := make(map[string]struct{})

	// Simulate each local transaction
	for _, txReq := range localTxs {
		for _, txBytes := range txReq.Transaction {
			tx := &types.Transaction{}
			if err := tx.UnmarshalBinary(txBytes); err != nil {
				return false, fmt.Errorf("failed to unmarshal transaction: %w", err)
			}

			traceResult, err := b.SimulateTransaction(ctx, tx, rpc.BlockNumberOrHashWithNumber(rpc.PendingBlockNumber))
			if err != nil {
				return false, fmt.Errorf("failed to simulate transaction: %w", err)
			}

			simState, err := mailboxProcessor.AnalyzeTransaction(traceResult, nil, nil, tx)
			if err != nil {
				return false, fmt.Errorf("failed to analyze transaction: %w", err)
			}

			coordinationStates = append(coordinationStates, simState)
			log.Info("[SSV] Transaction analyzed",
				"txHash", tx.Hash().Hex(),
				"requiresCoordination", simState.RequiresCoordination(),
				"dependencies", len(simState.Dependencies),
				"outbound", len(simState.OutboundMessages))

			// Pool successful transactions immediately
			_, done := txDone[tx.Hash().Hex()]
			if simState.Success && !done && len(simState.Dependencies) == 0 {
				log.Info("[SSV] Pooling successful transaction immediately", "hash", tx.Hash().Hex())
				b.poolPayloadTx(tx)
				txDone[tx.Hash().Hex()] = struct{}{}
			}
		}
	}

	allSentMsgs := make([]CrossRollupMessage, 0)
	allFulfilledDeps := make([]CrossRollupDependency, 0)
	putInboxHashes := make([]xt.ChainTxHash, 0)

	for _, state := range coordinationStates {
		if !state.RequiresCoordination() {
			continue
		}

		log.Info("[SSV] Transaction requires cross-rollup coordination",
			"txHash", state.Tx.Hash().Hex(),
			"dependencies", len(state.Dependencies),
			"outbound", len(state.OutboundMessages))

		sentMsgs, fulfilledDeps, err := mailboxProcessor.handleCrossRollupCoordination(ctx, state, xtID)
		if err != nil {
			return false, fmt.Errorf("failed to handle cross-rollup coordination: %w", err)
		}

		log.Info(
			"[SSV] Cross-rollup coordination completed",
			"xtID",
			xtID.Hex(),
			"sent",
			len(sentMsgs),
			"received",
			len(fulfilledDeps),
		)

		allSentMsgs = append(allSentMsgs, sentMsgs...)
		allFulfilledDeps = append(allFulfilledDeps, fulfilledDeps...)
	}

	// Create putInbox transactions for fulfilled dependencies
	if len(allFulfilledDeps) > 0 {
		log.Info("[SSV] Creating putInbox transactions for fulfilled dependencies", "count", len(allFulfilledDeps))

		nonce, err := b.GetPoolNonce(ctx, sequencerAddr)
		if err != nil {
			return false, fmt.Errorf("failed to get nonce: %w", err)
		}

		// Assign sequential nonces to putInbox starting from current poolNonce
		putInboxCount := uint64(len(allFulfilledDeps))
		putInboxNonces := make([]uint64, len(allFulfilledDeps))
		for i := range allFulfilledDeps {
			putInboxNonces[i] = nonce + uint64(i)
		}

		log.Info("[SSV] Assigning putInbox nonces from current pool nonce",
			"putInboxNonces", putInboxNonces,
			"poolNonce", nonce,
			"putInboxCount", putInboxCount)

		for i, dep := range allFulfilledDeps {
			putInboxNonce := putInboxNonces[i]
			putInboxTx, err := mailboxProcessor.createPutInboxTx(dep, putInboxNonce)
			if err != nil {
				return false, fmt.Errorf("failed to create putInbox transaction: %w", err)
			}

			if err := b.SubmitSequencerTransaction(ctx, putInboxTx, true); err != nil {
				return false, fmt.Errorf("failed to submit putInbox transaction: %w", err)
			}

			log.Info("[SSV] Created putInbox tx", "nonce", putInboxNonce, "txHash", putInboxTx.Hash().Hex())
			putInboxHashes = append(putInboxHashes, xt.ChainTxHash{
				ChainID: putInboxTx.ChainId().String(),
				Hash:    putInboxTx.Hash(),
			})
		}

		// Wait for putInbox transactions to be processed
		if err := b.waitForPutInboxTransactionsToBeProcessed(); err != nil {
			return false, fmt.Errorf("failed to wait for putInbox transactions: %w", err)
		}

		// Now that putInbox transactions are created, the pool nonce has advanced
		// We need to update original transactions that have colliding nonces
		newPoolNonce, err := b.GetPoolNonce(ctx, sequencerAddr)
		if err != nil {
			return false, fmt.Errorf("failed to get new pool nonce: %w", err)
		}

		log.Info("[SSV] Pool nonce after putInbox creation",
			"oldNonce", nonce,
			"newPoolNonce", newPoolNonce,
			"putInboxCount", putInboxCount)

		// Update any original transactions that have nonces conflicting with putInbox
		for i, state := range coordinationStates {
			txNonce := state.Tx.Nonce()
			// If this transaction's nonce conflicts with putInbox nonces, it needs to be updated
			if txNonce < newPoolNonce {
				log.Info("[SSV] Original transaction nonce conflicts with putInbox, needs update",
					"txHash", state.Tx.Hash().Hex(),
					"oldNonce", txNonce,
					"newPoolNonce", newPoolNonce)

				// Extract the transaction data to re-sign with new nonce
				// Note: This only works for handleOps transactions that WE signed, not raw user transactions
				from, err := types.Sender(types.LatestSignerForChainID(state.Tx.ChainId()), state.Tx)
				if err != nil {
					log.Error("[SSV] Failed to get sender for nonce update", "err", err)
					continue
				}

				// Only update if this is a sequencer transaction (that we signed)
				if from == sequencerAddr {
					// Create new transaction with updated nonce
					var newTxData types.TxData
					switch tx := state.Tx.Type(); tx {
					case types.DynamicFeeTxType:
						oldData := state.Tx
						newTxData = &types.DynamicFeeTx{
							ChainID:   oldData.ChainId(),
							Nonce:     newPoolNonce,
							GasTipCap: oldData.GasTipCap(),
							GasFeeCap: oldData.GasFeeCap(),
							Gas:       oldData.Gas(),
							To:        oldData.To(),
							Value:     oldData.Value(),
							Data:      oldData.Data(),
						}
					default:
						log.Warn("[SSV] Unsupported transaction type for nonce update", "type", tx)
						continue
					}

					newTx := types.NewTx(newTxData)
					signedTx, err := types.SignTx(newTx, types.NewLondonSigner(b.ChainConfig().ChainID), b.sequencerKey)
					if err != nil {
						log.Error("[SSV] Failed to re-sign transaction", "err", err)
						continue
					}

					oldTxHash := state.Tx.Hash()
					newTxHash := signedTx.Hash()

					log.Info("[SSV] Re-signed original transaction with new nonce",
						"oldTxHash", oldTxHash.Hex(),
						"newTxHash", newTxHash.Hex(),
						"oldNonce", txNonce,
						"newNonce", newPoolNonce)

					// Update the coordination state with new transaction
					coordinationStates[i].Tx = signedTx
					newPoolNonce++ // Increment for next transaction if needed

					// Send CrossChainTxResult to all participating chains (except ourselves)
					// so they can update their trackers with the correct hash mapping
					for _, txReq := range xtReq.Transactions {
						remoteChainID := new(big.Int).SetBytes(txReq.ChainId)
						if remoteChainID.Cmp(chainID) == 0 {
							continue // Don't send to ourselves
						}

						remoteChainIDKey := spconsensus.ChainKeyUint64(remoteChainID.Uint64())
						client, exists := b.sequencerClients[remoteChainIDKey]
						if !exists || client == nil {
							log.Warn("[SSV] No client for remote chain, cannot send CrossChainTxResult",
								"remoteChainID", remoteChainIDKey,
								"xtID", xtID.Hex())
							continue
						}

						txResultMsg := &rollupv1.CrossChainTxResult{
							XtId:         xtID,
							ChainId:      chainID.Bytes(),
							OriginalHash: oldTxHash.Bytes(),
							FinalHash:    newTxHash.Bytes(),
						}

						msg := &rollupv1.Message{
							SenderId: chainID.String(),
							Payload: &rollupv1.Message_CrossChainTxResult{
								CrossChainTxResult: txResultMsg,
							},
						}

						if err := client.Send(ctx, msg); err != nil {
							log.Error("[SSV] Failed to send CrossChainTxResult to remote chain",
								"remoteChainID", remoteChainIDKey,
								"xtID", xtID.Hex(),
								"err", err)
						} else {
							log.Info("[SSV] Sent CrossChainTxResult to remote chain",
								"remoteChainID", remoteChainIDKey,
								"xtID", xtID.Hex(),
								"oldHash", oldTxHash.Hex(),
								"newHash", newTxHash.Hex())
						}
					}
				}
			}
		}

		// Re-simulate after putInbox to detect ACK messages that need to be sent
		for i, state := range coordinationStates {
			traceResult, err := b.SimulateTransaction(
				ctx,
				state.Tx,
				rpc.BlockNumberOrHashWithNumber(rpc.PendingBlockNumber),
			)
			if err != nil {
				continue
			}

			newSimState, err := mailboxProcessor.AnalyzeTransaction(
				traceResult,
				allSentMsgs,
				allFulfilledDeps,
				state.Tx,
			)
			if err != nil {
				continue
			}

			log.Info("[SSV] Re-simulation mailbox state",
				"txHash", state.Tx.Hash().Hex(),
				"success", newSimState.Success,
				"deps", len(newSimState.Dependencies),
			)
			coordinationStates[i] = newSimState
			log.Info(
				"[SSV] Re-simulation successful for transaction",
				"txHash",
				state.Tx.Hash().Hex(),
				"xtID",
				xtID.Hex(),
			)

			// Send any ACK messages detected in re-simulation
			for _, outMsg := range newSimState.OutboundMessages {
				log.Info("[SSV] Detected new ACK message in re-simulation",
					"xtID", xtID.Hex(),
					"srcChain", outMsg.SourceChainID,
					"destChain", outMsg.DestChainID,
					"sessionId", outMsg.SessionID,
					"label", string(outMsg.Label))

				if err := mailboxProcessor.sendCIRCMessage(ctx, &outMsg, xtID); err != nil {
					log.Error("[SSV] Failed to send ACK CIRC message", "error", err, "xtID", xtID.Hex())
					continue
				}
			}

			if len(newSimState.OutboundMessages) > 0 {
				log.Info(
					"[SSV] Successfully sent ACK CIRC messages after putInbox",
					"count",
					len(newSimState.OutboundMessages),
					"xtID",
					xtID.Hex(),
				)
			}

			// Pool transactions immediately when they become successful
			_, done := txDone[state.Tx.Hash().Hex()]
			if newSimState.Success && !done && len(newSimState.Dependencies) == 0 {
				log.Info("[SSV] Pooling transaction after re-simulation", "hash", state.Tx.Hash().Hex())
				b.poolPayloadTx(state.Tx)
				txDone[state.Tx.Hash().Hex()] = struct{}{}
			}
		}
	}

	// Final check - pool any remaining successful transactions that weren't pooled yet
	for _, state := range coordinationStates {
		tx := state.Tx
		_, done := txDone[tx.Hash().Hex()]
		if state.Success && !done && len(state.Dependencies) == 0 {
			log.Info("[SSV] Pooling remaining successful transaction", "hash", tx.Hash().Hex())
			b.poolPayloadTx(tx)
			txDone[tx.Hash().Hex()] = struct{}{}
		}
	}

	// Check if all transactions are successful
	allSuccessful = successfulAll(coordinationStates)
	log.Info(
		"[SSV] SBCP simulation completed",
		"xtID",
		xtID.Hex(),
		"allSuccessful",
		allSuccessful,
		"pooled_original_txs",
		len(b.GetPendingOriginalTxs()),
	)

	// Deliver finalized transaction hashes to any local RPC subscriber.
	// For local chain transactions, use the potentially re-signed versions from coordinationStates.
	// For remote chain transactions, use the original hashes from xtReq.
	seen := make(map[common.Hash]struct{})
	results := make([]xt.ChainTxHash, 0)

	// Add local chain transactions (possibly re-signed with new nonces)
	// First, extract original hashes for comparison (in order they appear in coordinationStates)
	originalHashes := make([]common.Hash, 0)
	for _, txReq := range xtReq.Transactions {
		txChainID := new(big.Int).SetBytes(txReq.ChainId)
		if txChainID.Cmp(chainID) != 0 {
			continue
		}
		for _, txBytes := range txReq.Transaction {
			tx := &types.Transaction{}
			if err := tx.UnmarshalBinary(txBytes); err == nil {
				originalHashes = append(originalHashes, tx.Hash())
			}
		}
	}

	for i, state := range coordinationStates {
		if state.Tx == nil {
			continue
		}
		hash := state.Tx.Hash()
		if _, ok := seen[hash]; ok {
			continue
		}
		seen[hash] = struct{}{}

		var origHash common.Hash
		hasOrig := i < len(originalHashes)
		if hasOrig {
			origHash = originalHashes[i]
		}

		log.Info("[SSV] Adding local tx to results",
			"index", i,
			"returnedTxHash", hash.Hex(),
			"originalTxHash", func() string {
				if hasOrig {
					return origHash.Hex()
				}
				return "unknown"
			}(),
			"wasResigned", hasOrig && hash != origHash,
			"nonce", state.Tx.Nonce(),
			"chainID", chainID.String())

		results = append(results, xt.ChainTxHash{
			ChainID: chainID.String(),
			Hash:    hash,
		})
	}

	// Add remote chain transactions (not processed locally, use original hashes)
	for _, txReq := range xtReq.Transactions {
		txChainIDBytes := txReq.ChainId
		txChainID := new(big.Int).SetBytes(txChainIDBytes)

		// Skip local chain transactions - already added from coordinationStates
		if txChainID.Cmp(chainID) == 0 {
			continue
		}

		txChainIDStr := txChainID.String()
		for _, txBytes := range txReq.Transaction {
			tx := &types.Transaction{}
			if err := tx.UnmarshalBinary(txBytes); err != nil {
				log.Warn("[SSV] Failed to unmarshal remote transaction for result", "err", err)
				continue
			}

			hash := tx.Hash()
			if _, ok := seen[hash]; ok {
				continue
			}
			seen[hash] = struct{}{}
			results = append(results, xt.ChainTxHash{
				ChainID: txChainIDStr,
				Hash:    hash,
			})
		}
	}

	// Add putInbox transactions (these are local chain only)
	for _, item := range putInboxHashes {
		if _, ok := seen[item.Hash]; ok {
			continue
		}
		seen[item.Hash] = struct{}{}
		results = append(results, item)
	}

	log.Info("[SSV] Publishing XT results to tracker",
		"xtID", xtID.Hex(),
		"totalTxs", len(results),
		"localTxs", len(coordinationStates),
		"putInboxTxs", len(putInboxHashes))

	if b.xtTracker != nil {
		b.xtTracker.Publish(xtID, results)
	}
	trackerNotified = true

	if !allSuccessful {
		err = fmt.Errorf("sbc simulation unsuccessful for xt %s", xtID.Hex())
		return
	}

	return
}
