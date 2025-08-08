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
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/crypto"

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

	network "github.com/ethereum/go-ethereum/internal/publisherapi/spnetwork"
	spconsensus "github.com/ethereum/go-ethereum/internal/sp/consensus"
	spnetwork "github.com/ethereum/go-ethereum/internal/sp/network"
	sptypes "github.com/ethereum/go-ethereum/internal/sp/proto"
)

// EthAPIBackend implements ethapi.Backend and tracers.Backend for full nodes
type EthAPIBackend struct {
	extRPCEnabled       bool
	allowUnprotectedTxs bool
	disableTxPool       bool
	eth                 *Ethereum
	gpo                 *gasprice.Oracle

	// SSV: Shared publisher and coordinator
	spServer         network.Server
	spClient         network.Client
	coordinator      *spconsensus.Coordinator
	sequencerClients map[string]network.Client
	sequencerKey     *ecdsa.PrivateKey
	sequencerAddress common.Address

	// SSV: Sequencer transaction management
	pendingClearTx      *types.Transaction
	pendingPutInboxTxs  []*types.Transaction
	pendingSequencerTxs []*types.Transaction
	sequencerTxMutex    sync.RWMutex
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
		block, _, _ := b.eth.miner.Pending()
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

func (b *EthAPIBackend) HeaderByNumberOrHash(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*types.Header, error) {
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
		block, _, _ := b.eth.miner.Pending()
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

func (b *EthAPIBackend) BlockByNumberOrHash(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*types.Block, error) {
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
	return b.eth.miner.Pending()
}

func (b *EthAPIBackend) StateAndHeaderByNumber(ctx context.Context, number rpc.BlockNumber) (*state.StateDB, *types.Header, error) {
	// Pending state is only known by the miner
	if number == rpc.PendingBlockNumber {
		block, _, state := b.eth.miner.Pending()
		if block != nil && state != nil {
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

func (b *EthAPIBackend) StateAndHeaderByNumberOrHash(ctx context.Context, blockNrOrHash rpc.BlockNumberOrHash) (*state.StateDB, *types.Header, error) {
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

func (b *EthAPIBackend) GetEVM(ctx context.Context, state *state.StateDB, header *types.Header, vmConfig *vm.Config, blockCtx *vm.BlockContext) *vm.EVM {
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
		log.Warn("successfully sent tx to sequencer, but failed to persist in local tx pool", "err", err, "tx", signedTx.Hash())
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

func (b *EthAPIBackend) FeeHistory(ctx context.Context, blockCount uint64, lastBlock rpc.BlockNumber, rewardPercentiles []float64) (firstBlock *big.Int, reward [][]*big.Int, baseFee []*big.Int, gasUsedRatio []float64, baseFeePerBlobGas []*big.Int, blobGasUsedRatio []float64, err error) {
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

func (b *EthAPIBackend) StateAtBlock(ctx context.Context, block *types.Block, reexec uint64, base *state.StateDB, readOnly bool, preferDisk bool) (*state.StateDB, tracers.StateReleaseFunc, error) {
	return b.eth.stateAtBlock(ctx, block, reexec, base, readOnly, preferDisk)
}

func (b *EthAPIBackend) StateAtTransaction(ctx context.Context, block *types.Block, txIndex int, reexec uint64) (*types.Transaction, vm.BlockContext, *state.StateDB, tracers.StateReleaseFunc, error) {
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
func (b *EthAPIBackend) HandleSPMessage(ctx context.Context, msg *sptypes.Message) ([]common.Hash, error) {
	switch payload := msg.Payload.(type) {
	case *sptypes.Message_XtRequest:
		hashes, err := b.handleXtRequest(ctx, msg.SenderId, payload.XtRequest)
		if err != nil {
			return nil, fmt.Errorf("failed to handle xt request: %v", err)
		}

		return hashes, nil
	case *sptypes.Message_Decided:
		err := b.handleDecided(payload.Decided)
		if err != nil {
			return nil, fmt.Errorf("failed to handle decide: %v", err)
		}

		return nil, nil
	case *sptypes.Message_CircMessage:
		err := b.handleCIRCMessage(payload.CircMessage)
		if err != nil {
			return nil, fmt.Errorf("failed to handle circ message: %v", err)
		}

		return nil, nil
	default:
		log.Error("[SSV] Unknown message type", "type", fmt.Sprintf("%T", payload))
		return nil, fmt.Errorf("unknown message type: %T", payload)
	}
}

func (b *EthAPIBackend) isCoordinator(ctx context.Context, mailboxProcessor *MailboxProcessor) error {
	mailboxAddr := b.GetMailboxAddressFromChainID(b.ChainConfig().ChainID.Uint64())

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
		return fmt.Errorf("mailbox code not found")
	}

	coordinatorAddr, err := mailboxProcessor.getCoordinatorAddress(ctx, mailboxAddr)
	if err != nil {
		return err
	}

	if coordinatorAddr != b.sequencerAddress {
		return fmt.Errorf("sequencer is not coordinator")
	}

	return nil
}

// handleXtRequest processes a cross-chain transaction request.
// SSV
func (b *EthAPIBackend) handleXtRequest(ctx context.Context, from string, xtReq *sptypes.XTRequest) ([]common.Hash, error) {
	// Only start coordinator if this is actually a cross-chain transaction
	if len(xtReq.Transactions) > 1 {
		err := b.coordinator.StartTransaction(from, xtReq)
		if err != nil {
			return nil, err
		}
	}

	xtID, err := xtReq.XtID()
	if err != nil {
		return nil, err
	}

	chainID := b.ChainConfig().ChainID

	// Generate unique ID for this xTRequest (for 2PC tracking)
	xtRequestId := fmt.Sprintf("xt_%d_%s", time.Now().UnixNano(), from)
	log.Info("[SSV] Processing xTRequest", "id", xtRequestId, "senderID", from, "xtID", xtID.Hex())

	// Process each transaction for cross-rollup coordination
	localTxs := make([]*sptypes.TransactionRequest, 0)
	for _, txReq := range xtReq.Transactions {
		txChainID := new(big.Int).SetBytes(txReq.ChainId)

		if txChainID.Cmp(chainID) == 0 {
			localTxs = append(localTxs, txReq)
		} else {
			log.Info("[SSV] Received cross-chain transaction", "chainID", txChainID, "senderID", from, "txCount", len(txReq.Transaction))
		}
	}
	// Only proceed with coordination if we have local transactions
	if len(localTxs) == 0 {
		log.Info("[SSV] No local transactions to process", "xtID", xtID.Hex())
		return nil, nil
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

	// check if sequencer is coordinator
	if err = b.isCoordinator(ctx, mailboxProcessor); err != nil {
		log.Error("[SSV] Sequencer is not coordinator", "err", err)
		return nil, err
	}

	var newFulfilledDeps []CrossRollupDependency
	historicalSentCIRCMsgs := make([]CrossRollupMessage, 0)
	historicalCIRCDeps := make([]CrossRollupDependency, 0)

	startNonce, err := b.GetPoolNonce(ctx, sequencerAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to get nonce: %v", err)
	}

	// Populate mempool with payload txs
	//for _, txReq := range localTxs {
	//	for _, txBytes := range txReq.Transaction {
	//		tx := new(types.Transaction)
	//		if err := tx.UnmarshalBinary(txBytes); err != nil {
	//			return nil, err
	//		}
	//		b.poolPayloadTx(tx) // user tx
	//	}
	//}

	sequencerNonce := startNonce + 1 // preserve startNonce for clear() tx
	for {
		// Populate mempool with new putInbox txs
		for _, dep := range newFulfilledDeps {
			var putInboxTx *types.Transaction
			putInboxTx, err = mailboxProcessor.createPutInboxTx(dep, sequencerNonce)
			if err != nil {
				return nil, fmt.Errorf("failed to createAndSubmitPutInboxTx: %v", err)
			}

			err = b.SubmitSequencerTransaction(ctx, putInboxTx, true)
			if err != nil {
				return nil, fmt.Errorf("failed to SubmitSequencerTransaction (txHash=%s): %v", putInboxTx.Hash().Hex(), err)
			}

			sequencerNonce++
		}

		historicalCIRCDeps = append(historicalCIRCDeps, newFulfilledDeps...) // TODO: better refactor: use map[] or create new struct
		newFulfilledDeps = make([]CrossRollupDependency, 0)                  // reset fullfilled dependencies

		var coordinationStates []*SimulationState
		for _, txReq := range localTxs {
			log.Info("[SSV] Processing local transaction", "senderID", from, "chainID", b.ChainConfig().ChainID.String(), "txCount", len(txReq.Transaction))

			// Process each transaction
			for _, txBytes := range txReq.Transaction {
				tx := new(types.Transaction)
				if err := tx.UnmarshalBinary(txBytes); err != nil {
					return nil, err
				}

				// SIMULATE
				traceResult, err := b.SimulateTransaction(ctx, tx, rpc.BlockNumberOrHashWithNumber(rpc.PendingBlockNumber))
				if err != nil {
					log.Error("[SSV] Cross-chain transaction simulation failed", "txHash", tx.Hash().Hex(), "error", err)
					return nil, fmt.Errorf("simulation failed: %w", err)
				}

				log.Info("[SSV] Transaction simulated", "txHash", tx.Hash().Hex())

				// ANALYZE
				log.Info("[SSV] Analyzing cross-rollup transaction", "txHash", tx.Hash().Hex(), "xtRequestId", xtRequestId)
				simState, err := mailboxProcessor.AnalyzeTransaction(traceResult, historicalSentCIRCMsgs, historicalCIRCDeps, tx.Hash().Hex())
				if err != nil {
					log.Error("[SSV] Failed to process transaction", "error", err, "txHash", tx.Hash().Hex())
					// Vote abort if processing fails
					_, err = b.coordinator.RecordVote(xtID, chainID.Text(16), false)
					return nil, err
				}
				coordinationStates = append(coordinationStates, simState)

				log.Info("[SSV] Transaction analyzed", "txHash", tx.Hash().Hex(), "requiresCoordination", simState.RequiresCoordination(), "dependencies", len(simState.Dependencies), "outbound", len(simState.OutboundMessages))
			}
		}

		logSummary(xtRequestId, xtID, coordinationStates)

		// successful when EVM does not end up with reverted()
		if successful(coordinationStates) {
			_, err = b.coordinator.RecordVote(xtID, chainID.Text(16), true)
			if err != nil {
				return nil, err
			}
		}

		if requiresCoordination(coordinationStates) {
			// Handle cross-rollup coordination for each transaction that needs it
			for _, state := range coordinationStates {
				if state.RequiresCoordination() {
					var sentOutboundMsgs []CrossRollupMessage
					var fulFilledDeps []CrossRollupDependency
					sentOutboundMsgs, fulFilledDeps, err = mailboxProcessor.handleCrossRollupCoordination(ctx, state, xtID)
					if err != nil {
						log.Error("[SSV] Cross-rollup coordination failed", "error", err, "xtID", xtID.Hex())
						// Vote abort if coordination fails
						_, err = b.coordinator.RecordVote(xtID, chainID.Text(16), false)
						return nil, err
					}

					newFulfilledDeps = append(newFulfilledDeps, fulFilledDeps...)
					historicalSentCIRCMsgs = append(historicalSentCIRCMsgs, sentOutboundMsgs...)
				}
			}

			log.Info("[SSV] Cross-rollup coordination phase completed", "xtID", xtID.Hex())
		} else {
			log.Info("[SSV] No coordination required, voting commit", "xtID", xtID.Hex())
			_, err = b.coordinator.RecordVote(xtID, chainID.Text(16), true)
			if err != nil {
				return nil, err
			}
			return nil, nil
		}
	}
}

func successful(coordinationStates []*SimulationState) bool {
	for _, s := range coordinationStates {
		if !s.OriginalSuccess {
			return false
		}
	}

	return true
}

func ToString(success bool) string {
	if success {
		return "successful"
	}
	return "failed"
}

func logSummary(xtRequestId string, xtID *sptypes.XtID, coordinationStates []*SimulationState) {
	totalDeps := 0
	totalOutbound := 0
	successfulStates := 0

	for _, state := range coordinationStates {
		totalDeps += len(state.Dependencies)
		totalOutbound += len(state.OutboundMessages)
		if state.OriginalSuccess {
			successfulStates++
		}
	}

	log.Info("[SSV] xTRequest coordination summary",
		"id", xtRequestId,
		"xtID", xtID.Hex(),
		"requiresCoordination", requiresCoordination(coordinationStates),
		"totalDependencies", totalDeps,
		"totalOutbound", totalOutbound,
		"successfulStates", successfulStates,
		"totalStates", len(coordinationStates),
	)
}

func requiresCoordination(coordinationStates []*SimulationState) bool {
	for _, s := range coordinationStates {
		if s.RequiresCoordination() {
			return true
		}
	}

	return false
}

// handleDecided processes a Decided message received from the shared publisher.
// SSV
func (b *EthAPIBackend) handleDecided(xtDecision *sptypes.Decided) error {
	return b.coordinator.RecordDecision(xtDecision.XtId, xtDecision.GetDecision())
}

// handleCIRCMessage processes a CIRC message received from the shared publisher.
// SSV
func (b *EthAPIBackend) handleCIRCMessage(circMessage *sptypes.CIRCMessage) error {
	return b.coordinator.RecordCIRCMessage(circMessage)
}

// StartCallbackFn returns a function that can be used to send transaction bundles to the shared publisher.
// SSV
func (b *EthAPIBackend) StartCallbackFn(chainID *big.Int) spconsensus.StartFn {
	return func(ctx context.Context, from string, xtReq *sptypes.XTRequest) error {
		if from != spnetwork.SharedPublisherSenderID {
			spMsg := &sptypes.Message{
				SenderId: chainID.String(),
				Payload: &sptypes.Message_XtRequest{
					XtRequest: xtReq,
				},
			}
			err := b.spClient.Send(ctx, spMsg)
			if err != nil {
				log.Error("Failed to send transaction bundle to shared publisher", "err", err)
			}
		}
		return nil
	}
}

// VoteCallbackFn returns a function that can be used to send votes for cross-chain transactions.
// SSV
func (b *EthAPIBackend) VoteCallbackFn(chainID *big.Int) spconsensus.VoteFn {
	return func(ctx context.Context, xtID *sptypes.XtID, vote bool) error {
		msgVote := &sptypes.Message_Vote{
			Vote: &sptypes.Vote{
				Vote:          vote,
				XtId:          xtID,
				SenderChainId: chainID.Bytes(),
			},
		}

		spMsg := &sptypes.Message{
			SenderId: chainID.String(),
			Payload:  msgVote,
		}
		return b.spClient.Send(ctx, spMsg)
	}
}

// SimulateTransactionWithSSVTrace simulates a transaction and returns SSV trace data.
// SSV
//func (b *EthAPIBackend) SimulateTransaction(ctx context.Context, tx *types.Transaction, blockNrOrHash rpc.BlockNumberOrHash) (*ssv.SSVTraceResult, error) {
//	timer := time.Now()
//	defer func() {
//		log.Info("[SSV] Simulated transaction with SSV trace", "txHash", tx.Hash().Hex(), "duration", time.Since(timer))
//	}()
//
//	stateDB, header, err := b.StateAndHeaderByNumberOrHash(ctx, blockNrOrHash)
//	if err != nil {
//		return nil, err
//	}
//
//	snapshot := stateDB.Snapshot()
//	defer stateDB.RevertToSnapshot(snapshot)
//
//	signer := types.MakeSigner(b.ChainConfig(), header.Number, header.Time)
//	msg, err := core.TransactionToMessage(tx, signer, header.BaseFee)
//	if err != nil {
//		return nil, err
//	}
//
//	mailboxAddresses := b.GetMailboxAddresses()
//	tracer := native.NewSSVTracer(mailboxAddresses)
//
//	vmConfig := vm.Config{}
//	if b.eth.blockchain.GetVMConfig() != nil {
//		vmConfig = *b.eth.blockchain.GetVMConfig()
//	}
//	vmConfig.Tracer = tracer.Hooks()
//	vmConfig.EnablePreimageRecording = true
//
//	blockContext := core.NewEVMBlockContext(header, b.eth.blockchain, nil, b.ChainConfig(), stateDB)
//	evm := vm.NewEVM(blockContext, stateDB, b.ChainConfig(), vmConfig)
//
//	result, err := core.ApplyMessage(evm, msg, new(core.GasPool).AddGas(header.GasLimit))
//	if err != nil {
//		return nil, err
//	}
//
//	traceResult := tracer.GetTraceResult()
//	traceResult.ExecutionResult = result
//
//	return traceResult, nil
//}

func (b *EthAPIBackend) SimulateTransaction(ctx context.Context, tx *types.Transaction, blockNrOrHash rpc.BlockNumberOrHash) (*ssv.SSVTraceResult, error) {
	timer := time.Now()
	defer func() {
		log.Info("[SSV] Simulated transaction with SSV trace", "txHash", tx.Hash().Hex(), "duration", time.Since(timer))
	}()

	//b.poolPayloadTx(tx)

	stateDB, header, err := b.StateAndHeaderByNumberOrHash(ctx, blockNrOrHash)
	if err != nil {
		return nil, err
	}

	log.Info("[DEBUG] Block info",
		"requestedBlock", blockNrOrHash,
		"returnedBlockNumber", header.Number,
		"returnedBlockHash", header.Hash(),
		"txIndex", stateDB.TxIndex())

	snapshot := stateDB.Snapshot()
	defer stateDB.RevertToSnapshot(snapshot)

	signer := types.MakeSigner(b.ChainConfig(), header.Number, header.Time)
	msg, err := core.TransactionToMessage(tx, signer, header.BaseFee)
	if err != nil {
		return nil, err
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

	stateDB.SetTxContext(tx.Hash(), stateDB.TxIndex()+1)

	gasPool := new(core.GasPool).AddGas(header.GasLimit)
	result, err := core.ApplyMessage(evm, msg, gasPool)
	if err != nil {
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
	} else {
		b.SetPendingClearTx(tx)
		log.Info("[SSV] Set clear transaction to mempool", "txHash", tx.Hash().Hex())
	}

	// FIXME: this should fail for now (invalid sender: invalid transaction v, r, s value)
	//return b.sendTx(ctx, tx)
	return nil
}

// GetMailboxAddresses returns the list of mailbox contract addresses to watch.package ethapi
// SSV
func (b *EthAPIBackend) GetMailboxAddresses() []common.Address {
	return []common.Address{
		common.HexToAddress(native.RollupAMailBoxAddr),
		common.HexToAddress(native.RollupBMailBoxAddr),
	}
}

func (b *EthAPIBackend) GetMailboxAddressFromChainID(chainID uint64) common.Address {
	switch chainID {
	case 11111:
		return common.HexToAddress(native.RollupAMailBoxAddr)
	case 22222:
		return common.HexToAddress(native.RollupBMailBoxAddr)
	default:
		return common.Address{}
	}
}

// GetPendingClearTx returns the pending clear transaction for the current block.
// SSV
func (b *EthAPIBackend) GetPendingClearTx() *types.Transaction {
	b.sequencerTxMutex.RLock()
	defer b.sequencerTxMutex.RUnlock()
	return b.pendingClearTx
}

// SetPendingClearTx sets the clear transaction for the current block.
// SSV
func (b *EthAPIBackend) SetPendingClearTx(tx *types.Transaction) {
	b.sequencerTxMutex.Lock()
	defer b.sequencerTxMutex.Unlock()
	b.pendingClearTx = tx
}

// AddPendingPutInboxTx adds a putInbox transaction to the pending list.
// SSV
func (b *EthAPIBackend) AddPendingPutInboxTx(tx *types.Transaction) {
	b.sequencerTxMutex.Lock()
	defer b.sequencerTxMutex.Unlock()

	b.pendingPutInboxTxs = append(b.pendingPutInboxTxs, tx)

	log.Info("[SSV] Added pending putInbox transaction",
		"txHash", tx.Hash().Hex(),
		"totalPending", len(b.pendingPutInboxTxs))
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

// ClearSequencerTransactions clears all pending sequencer transactions (called after block creation).
// SSV
func (b *EthAPIBackend) ClearSequencerTransactions() {
	b.sequencerTxMutex.Lock()
	defer b.sequencerTxMutex.Unlock()

	b.pendingClearTx = nil
	b.pendingPutInboxTxs = b.pendingPutInboxTxs[:0] // Clear slice but keep capacity

	log.Debug("[SSV] Cleared pending sequencer transactions")
}

// ClearSequencerTransactionsAfterBlock clears all pending sequencer transactions after block creation
// SSV
func (b *EthAPIBackend) ClearSequencerTransactionsAfterBlock() {
	b.sequencerTxMutex.Lock()
	defer b.sequencerTxMutex.Unlock()

	log.Info("[SSV] Clearing sequencer transactions",
		"clearTx", b.pendingClearTx != nil,
		"putInboxCount", len(b.pendingPutInboxTxs),
		"originalCount", len(b.pendingSequencerTxs))

	b.pendingClearTx = nil
	b.pendingPutInboxTxs = nil
	b.pendingSequencerTxs = nil
}

// PrepareSequencerTransactionsForBlock prepares sequencer transactions for inclusion in a new block
// SSV
func (b *EthAPIBackend) PrepareSequencerTransactionsForBlock(ctx context.Context) error {
	log.Info("[SSV] Preparing sequencer transactions for new block")

	// 1. Create clear transaction if we have any cross-chain activity
	if len(b.GetPendingPutInboxTxs()) > 0 || b.shouldCreateClearTx() {
		clearTx, err := b.createClearTransaction(ctx)
		if err != nil {
			log.Error("[SSV] Failed to create clear transaction", "err", err)
			return err
		}

		b.SetPendingClearTx(clearTx)
		log.Info("[SSV] Created clear transaction", "txHash", clearTx.Hash().Hex(), "nonce", clearTx.Nonce(), "pendingPutInboxTxs", len(b.GetPendingPutInboxTxs()), "shouldCreate", b.shouldCreateClearTx())
	}

	return nil
}

// shouldCreateClearTx determines if we need a clear transaction
// SSV
func (b *EthAPIBackend) shouldCreateClearTx() bool {
	return len(b.GetPendingOriginalTxs()) > 0
}

// createClearTransaction creates a transaction to clear the mailbox
// SSV
func (b *EthAPIBackend) createClearTransaction(ctx context.Context) (*types.Transaction, error) {
	nonce, err := b.GetPoolNonce(ctx, b.sequencerAddress)
	if err != nil {
		return nil, err
	}

	parsedABI, err := abi.JSON(strings.NewReader(mailboxABI))
	if err != nil {
		return nil, err
	}

	callData, err := parsedABI.Pack("clear")
	if err != nil {
		return nil, fmt.Errorf("failed to prepare calldata for \"clear\" method: %v", err)
	}

	var mailboxAddr common.Address
	chainID := b.ChainConfig().ChainID.Int64()
	switch chainID {
	case 11111:
		mailboxAddr = b.GetMailboxAddresses()[0]
	case 22222:
		mailboxAddr = b.GetMailboxAddresses()[1]
	default:
		return nil, fmt.Errorf("unable to select mailbox addr. Unsupported \"%d\"chain id", chainID)
	}

	txData := &types.DynamicFeeTx{
		ChainID:    b.ChainConfig().ChainID,
		Nonce:      nonce,
		GasTipCap:  big.NewInt(1000000000),
		GasFeeCap:  big.NewInt(20000000000),
		Gas:        300000,
		To:         &mailboxAddr,
		Value:      big.NewInt(0),
		Data:       callData,
		AccessList: nil,
	}

	tx := types.NewTx(txData)
	signedTx, err := types.SignTx(tx, types.NewLondonSigner(b.ChainConfig().ChainID), b.sequencerKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign tx %v", err)
	}

	return signedTx, nil
}

// GetOrderedTransactionsForBlock returns transactions in the correct order for block inclusion
// SSV
func (b *EthAPIBackend) GetOrderedTransactionsForBlock(ctx context.Context, normalTxs types.Transactions) (types.Transactions, error) {
	var orderedTxs types.Transactions

	// 1. First: Clear transaction
	if clearTx := b.GetPendingClearTx(); clearTx != nil {
		orderedTxs = append(orderedTxs, clearTx)
		log.Info("[SSV] Added clear transaction to block", "txHash", clearTx.Hash().Hex())
	}

	// 2. Second: All putInbox transactions
	putInboxTxs := b.GetPendingPutInboxTxs()
	if len(putInboxTxs) > 0 {
		orderedTxs = append(orderedTxs, putInboxTxs...)
		log.Info("[SSV] Added putInbox transactions to block", "count", len(putInboxTxs))

		for i, tx := range putInboxTxs {
			log.Info("[SSV] PutInbox transaction", "index", i, "txHash", tx.Hash().Hex())
		}
	}

	// 3. Third: Normal user transactions (excluding any sequencer txs that might be in pool)
	filteredNormalTxs := b.filterOutSequencerTransactions(normalTxs)
	orderedTxs = append(orderedTxs, filteredNormalTxs...)

	log.Info("[SSV] Block transaction order finalized",
		"clearTxs", func() int {
			if b.GetPendingClearTx() != nil {
				return 1
			}
			return 0
		}(),
		"putInboxTxs", len(putInboxTxs),
		"normalTxs", len(filteredNormalTxs),
		"totalOrdered", len(orderedTxs),
	)

	return orderedTxs, nil
}

// filterOutSequencerTransactions removes sequencer transactions from normal transaction list
// SSV
func (b *EthAPIBackend) filterOutSequencerTransactions(txs types.Transactions) types.Transactions {
	var filtered types.Transactions
	sequencerTxHashes := make(map[common.Hash]bool)

	// Build map of sequencer transaction hashes
	if clearTx := b.GetPendingClearTx(); clearTx != nil {
		sequencerTxHashes[clearTx.Hash()] = true
	}

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
	log.Info("[SSV] Block building started - preparing sequencer state")

	return nil
}

// OnBlockBuildingComplete is called when block building completes
// SSV
func (b *EthAPIBackend) OnBlockBuildingComplete(ctx context.Context, block *types.Block, success bool) error {
	if success && block != nil {
		log.Info("[SSV] Block building completed successfully", "blockHash", block.Hash().Hex())

		pendingPutInbox := b.GetPendingPutInboxTxs()
		containsPutInbox := false

		if len(pendingPutInbox) > 0 {
			putInboxMap := make(map[common.Hash]bool)
			for _, ptx := range pendingPutInbox {
				putInboxMap[ptx.Hash()] = true
			}

			for _, btx := range block.Transactions() {
				if putInboxMap[btx.Hash()] {
					containsPutInbox = true
					break
				}
			}
		}

		// Only notify coordinator for blocks with putInbox transactions
		if containsPutInbox {
			if err := b.coordinator.HandleBlockReady(ctx, block); err != nil {
				log.Error("[SSV] Coordinator failed to handle block", "err", err, "blockHash", block.Hash().Hex())
			}
		}

		b.ClearSequencerTransactionsAfterBlock()
		log.Info("[SSV] Sequencer transaction state cleared after successful block")
	}
	return nil
}

func (b *EthAPIBackend) BlockCallbackFn() func(ctx context.Context, block *types.Block, xtIDs []*sptypes.XtID) error {
	return func(ctx context.Context, block *types.Block, xtIDs []*sptypes.XtID) error {

		var buf bytes.Buffer
		if err := block.EncodeRLP(&buf); err != nil {
			return fmt.Errorf("failed to RLP encode block: %w", err)
		}
		blockData := buf.Bytes()

		blockMsg := &sptypes.Block{
			ChainId:       b.ChainConfig().ChainID.Bytes(),
			BlockData:     blockData,
			IncludedXtIds: xtIDs,
		}

		spMsg := &sptypes.Message{
			SenderId: b.ChainConfig().ChainID.String(),
			Payload: &sptypes.Message_Block{
				Block: blockMsg,
			},
		}

		log.Info("[SSV] Sending block to shared publisher", "blockHash", block.Hash().Hex(), "xtIDs", len(xtIDs))

		err := b.spClient.Send(ctx, spMsg)

		if err != nil {
			log.Error("[SSV] Failed to send block to shared publisher", "err", err, "blockHash", block.Hash().Hex())
			return fmt.Errorf("failed to send block to shared publisher: %w", err)
		}

		log.Info("[SSV] Block sent to shared publisher successfully", "blockHash", block.Hash().Hex(), "xtIDs", len(xtIDs))
		return nil
	}
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
// In api_backend.go, update reSimulateAfterMailboxPopulation:
func (b *EthAPIBackend) reSimulateAfterMailboxPopulation(ctx context.Context, xtReq *sptypes.XTRequest, xtID *sptypes.XtID, coordinationStates []*SimulationState) (bool, error) {
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

	// Re-simulate each local transaction against PENDING state
	// TODO: confirm? (pending instead of latest block)
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
				log.Error("[SSV] Failed to unmarshal transaction for re-simulation",
					"error", err,
					"index", i,
					"xtID", xtID.Hex())
				allSuccessful = false
				continue
			}

			// Re-simulate the transaction
			success, err := b.reSimulateTransaction(ctx, tx, blockNrOrHash, xtID)
			if err != nil {
				log.Error("[SSV] Re-simulation error",
					"txHash", tx.Hash().Hex(),
					"error", err,
					"xtID", xtID.Hex())
				allSuccessful = false
				continue
			}

			if !success {
				log.Warn("[SSV] Re-simulation failed for transaction",
					"txHash", tx.Hash().Hex(),
					"xtID", xtID.Hex())
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
func (b *EthAPIBackend) reSimulateTransaction(ctx context.Context, tx *types.Transaction, blockNrOrHash rpc.BlockNumberOrHash, xtID *sptypes.XtID) (bool, error) {
	log.Debug("[SSV] Re-simulating transaction",
		"txHash", tx.Hash().Hex(),
		"xtID", xtID.Hex())

	// Simulate with SSV tracing to detect mailbox interactions
	traceResult, err := b.SimulateTransaction(ctx, tx, blockNrOrHash)
	if err != nil {
		log.Error("[SSV] Transaction simulation with trace failed",
			"txHash", tx.Hash().Hex(),
			"error", err,
			"xtID", xtID.Hex())
		return false, err
	}

	// Check if execution was successful
	if traceResult.ExecutionResult.Err != nil {
		log.Warn("[SSV] Transaction execution failed in re-simulation",
			"txHash", tx.Hash().Hex(),
			"executionError", traceResult.ExecutionResult.Err,
			"xtID", xtID.Hex())
		return false, nil
	}

	// Validate that the transaction used reasonable gas (not failed silently)
	if traceResult.ExecutionResult.UsedGas == 0 {
		log.Warn("[SSV] Transaction used no gas, likely failed silently",
			"txHash", tx.Hash().Hex(),
			"xtID", xtID.Hex())
		return false, nil
	}

	// Check that mailbox operations were traced (indicating they succeeded)
	if len(traceResult.Operations) == 0 {
		log.Warn("[SSV] No mailbox operations detected in re-simulation",
			"txHash", tx.Hash().Hex(),
			"xtID", xtID.Hex())
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
	log.Info("[SSV] Added payload transaction to pendingSequencerTxs", "hash", tx.Hash().Hex(), "count", len(b.pendingSequencerTxs))
}
