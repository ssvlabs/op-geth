package l1

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/rs/zerolog"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/l1/contracts"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/l1/events"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/l1/tx"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/store"
)

// EthPublisher publishes superblocks to Ethereum L1 using go-ethereum.
// It implements the Publisher interface and uses EIP-1559 by default.
type EthPublisher struct {
	cfg      Config
	client   ethClient
	signer   Signer
	contract contracts.Binding
	log      zerolog.Logger
}

// NewEthPublisher connects to the RPC endpoint and prepares an EthPublisher.
// The signer may be nil if Config.PrivateKeyHex is set.
func NewEthPublisher(
	ctx context.Context,
	cfg Config,
	contract contracts.Binding,
	signer Signer,
	log zerolog.Logger,
) (*EthPublisher, error) {
	if contract == nil {
		return nil, fmt.Errorf("contract binding must be provided")
	}
	if cfg.RPCEndpoint == "" {
		return nil, fmt.Errorf("rpc_endpoint must be provided")
	}

	// Dial with auto-protocol selection (http/ws)
	rpcClient, err := rpc.DialContext(ctx, cfg.RPCEndpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to dial RPC: %w", err)
	}
	gethClient := ethclient.NewClient(rpcClient)

	// Resolve chainID
	rpcChainID, err := gethClient.ChainID(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch chain id: %w", err)
	}
	if cfg.ChainID == 0 {
		cfg.ChainID = rpcChainID.Uint64()
		log.Info().Uint64("chain_id", cfg.ChainID).Msg("Auto-detected chain ID")
	} else if cfg.ChainID != rpcChainID.Uint64() {
		log.Warn().
			Uint64("config_chain_id", cfg.ChainID).
			Stringer("rpc_chain_id", rpcChainID).
			Msg("Configured chain ID differs from RPC endpoint; using configured value")
	}

	// Build local signer if not provided
	if signer == nil && cfg.PrivateKeyHex != "" {
		keyHex := strings.TrimPrefix(cfg.PrivateKeyHex, "0x")
		keyBytes, err := hex.DecodeString(keyHex)
		if err != nil {
			return nil, fmt.Errorf("invalid private key hex: %w", err)
		}
		privKey, err := cryptoToECDSA(keyBytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse private key: %w", err)
		}
		signer = NewLocalECDSASigner(new(big.Int).SetUint64(cfg.ChainID), privKey)
	}
	if signer == nil {
		return nil, fmt.Errorf("no signer provided; set PrivateKeyHex or pass a Signer")
	}

	ep := &EthPublisher{
		cfg:      cfg,
		client:   gethClient,
		signer:   signer,
		contract: contract,
		log:      log.With().Str("component", "l1-eth-publisher").Logger(),
	}
	return ep, nil
}

// PublishSuperblock constructs, signs, and broadcasts a transaction
// that calls the configured Superblock contract with the encoded superblock data.
func (p *EthPublisher) PublishSuperblock(ctx context.Context, superblock *store.Superblock) (*tx.Transaction, error) {
	p.log.Info().Uint64("superblock_number", superblock.Number).Msg("Building superblock publish transaction")

	calldata, err := p.contract.BuildPublishCalldata(ctx, superblock)
	if err != nil {
		p.log.Error().Err(err).Uint64("superblock_number", superblock.Number).Msg("Failed to build calldata")
		return nil, fmt.Errorf("build calldata: %w", err)
	}

	from := p.signer.From()
	to := p.contract.Address()

	// Nonce
	nonce, err := p.client.PendingNonceAt(ctx, from)
	if err != nil {
		p.log.Error().Err(err).Str("from", from.Hex()).Msg("Failed to fetch nonce")
		return nil, fmt.Errorf("fetch nonce: %w", err)
	}

	// Gas + fees
	gasLimit := p.estimateGasLimit(ctx, from, to, calldata)
	tipCap, feeCap := p.suggestFees(ctx)

	// Build tx
	txData := &types.DynamicFeeTx{
		ChainID:   big.NewInt(int64(p.cfg.ChainID)),
		Nonce:     nonce,
		To:        &to,
		Value:     big.NewInt(0),
		Gas:       gasLimit,
		GasTipCap: tipCap,
		GasFeeCap: feeCap,
		Data:      calldata,
	}
	unsigned := types.NewTx(txData)

	signed, err := p.signer.SignTx(ctx, unsigned)
	if err != nil {
		return nil, fmt.Errorf("sign tx: %w", err)
	}

	if err := p.client.SendTransaction(ctx, signed); err != nil {
		p.log.Error().Err(err).
			Str("tx_hash", signed.Hash().Hex()).
			Uint64("superblock_number", superblock.Number).
			Msg("Failed to send transaction")
		return nil, fmt.Errorf("send tx: %w", err)
	}

	p.log.Info().
		Str("tx_hash", signed.Hash().Hex()).
		Uint64("nonce", nonce).
		Uint64("gas_limit", gasLimit).
		Str("gas_tip_cap", tipCap.String()).
		Str("gas_fee_cap", feeCap.String()).
		Uint64("superblock_number", superblock.Number).
		Msg("Successfully submitted superblock transaction")

	return &tx.Transaction{
		Hash:      signed.Hash().Bytes(),
		Nonce:     nonce,
		GasPrice:  0, // dynamic fees
		GasLimit:  gasLimit,
		Data:      calldata,
		Timestamp: time.Now(),
	}, nil
}

// estimateGasLimit estimates gas and applies safety buffer
func (p *EthPublisher) estimateGasLimit(ctx context.Context, from, to common.Address, calldata []byte) uint64 {
	gasMsg := ethereum.CallMsg{From: from, To: &to, Value: big.NewInt(0), Data: calldata}
	if est, err := p.client.EstimateGas(ctx, gasMsg); err == nil {
		buffer := est * p.cfg.GasLimitBufferPct / 100
		p.log.Debug().Uint64("estimated_gas", est).Uint64("gas_limit", est+buffer).Msg("Gas estimated")
		return est + buffer
	}
	p.log.Warn().Uint64("fallback_gas_limit", 1_500_000).Msg("Gas estimation failed, using fallback")
	return 1_500_000
}

// suggestFees returns EIP-1559 tip and fee caps with config overrides
func (p *EthPublisher) suggestFees(ctx context.Context) (*big.Int, *big.Int) {
	head, _ := p.client.HeaderByNumber(ctx, nil)
	tipCap, err := p.client.SuggestGasTipCap(ctx)
	if err != nil || tipCap == nil {
		tipCap = big.NewInt(2_000_000_000)
	}
	var feeCap *big.Int
	if head != nil && head.BaseFee != nil {
		feeCap = new(big.Int).Add(new(big.Int).Mul(head.BaseFee, big.NewInt(2)), tipCap)
	} else if sp, err := p.client.SuggestGasPrice(ctx); err == nil && sp != nil {
		feeCap = sp
	} else {
		feeCap = new(big.Int).Add(big.NewInt(2_000_000_000), tipCap)
	}
	if p.cfg.MaxPriorityFeeWei != "" {
		if v, ok := new(big.Int).SetString(p.cfg.MaxPriorityFeeWei, 10); ok && v.Sign() > 0 && v.Cmp(tipCap) < 0 {
			tipCap = v
		}
	}
	if p.cfg.MaxFeePerGasWei != "" {
		if v, ok := new(big.Int).SetString(p.cfg.MaxFeePerGasWei, 10); ok && v.Sign() > 0 && v.Cmp(feeCap) < 0 {
			feeCap = v
		}
	}
	return tipCap, feeCap
}

// GetPublishStatus queries the transaction receipt and returns a normalized status.
func (p *EthPublisher) GetPublishStatus(ctx context.Context, txHash []byte) (*tx.TransactionStatus, error) {
	hash := common.BytesToHash(txHash)

	receipt, err := p.client.TransactionReceipt(ctx, hash)
	if err != nil {
		// In case of not found, consider pending
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			p.log.Debug().Str("tx_hash", hash.Hex()).Msg("Transaction not found, considering as pending")
			return &tx.TransactionStatus{Hash: txHash, Status: tx.TransactionStatePending}, nil
		}
		p.log.Error().Err(err).Str("tx_hash", hash.Hex()).Msg("Failed to get transaction receipt")
		return nil, fmt.Errorf("get receipt: %w", err)
	}

	status := &tx.TransactionStatus{
		Hash:        txHash,
		BlockNumber: receipt.BlockNumber.Uint64(),
		BlockHash:   receipt.BlockHash.Bytes(),
		GasUsed:     receipt.GasUsed,
	}

	if receipt.Status == types.ReceiptStatusFailed {
		status.Status = tx.TransactionStateFailed
		p.log.Warn().
			Str("tx_hash", hash.Hex()).
			Uint64("block_number", status.BlockNumber).
			Uint64("gas_used", status.GasUsed).
			Msg("Transaction failed")
		return status, nil
	}

	// included, compute confirmations
	head, err := p.client.HeaderByNumber(ctx, nil)
	if err != nil {
		p.log.Warn().Err(err).Str("tx_hash", hash.Hex()).Msg("Failed to get latest block for confirmation count")
		status.Status = tx.TransactionStateIncluded
		return status, nil
	}
	if head.Number.Uint64() <= status.BlockNumber {
		status.Status = tx.TransactionStateIncluded
		status.ConfirmationCount = 0
		return status, nil
	}
	confs := head.Number.Uint64() - status.BlockNumber
	status.ConfirmationCount = int(confs)

	switch {
	case confs >= p.cfg.FinalityDepth:
		status.Status = tx.TransactionStateFinalized
		p.log.Debug().
			Str("tx_hash", hash.Hex()).
			Int("confirmations", status.ConfirmationCount).
			Msg("Transaction finalized")
	case confs >= p.cfg.Confirmations:
		status.Status = tx.TransactionStateConfirmed
		p.log.Debug().
			Str("tx_hash", hash.Hex()).
			Int("confirmations", status.ConfirmationCount).
			Msg("Transaction confirmed")
	default:
		status.Status = tx.TransactionStateIncluded
		p.log.Debug().
			Str("tx_hash", hash.Hex()).
			Int("confirmations", status.ConfirmationCount).
			Msg("Transaction included")
	}
	return status, nil
}

// WatchSuperblocks subscribes to contract logs and maps them into SuperblockEvent.
// Without the ABI and concrete event signatures, this returns a closed channel for now.
func (p *EthPublisher) WatchSuperblocks(ctx context.Context) (<-chan *events.SuperblockEvent, error) {
	// Require contract to expose ABI for event decoding (L2OutputOracleBinding)
	type abiProvider interface{ ABI() abi.ABI }
	ap, ok := p.contract.(abiProvider)
	if !ok {
		p.log.Error().Msg("Contract binding does not expose ABI for events")
		return nil, fmt.Errorf("contract binding does not expose ABI for events")
	}

	p.log.Info().Str("contract_address", p.contract.Address().Hex()).Msg("Starting superblock event watcher")
	evCh, err := events.WatchOutputProposed(ctx, p.client, p.contract.Address(), ap.ABI())
	if err != nil {
		p.log.Error().Err(err).Str("contract_address", p.contract.Address().Hex()).Msg("Failed to start event watcher")
		return nil, err
	}

	out := make(chan *events.SuperblockEvent, 128)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-evCh:
				if !ok {
					return
				}
				p.log.Info().
					Str("event_type", string(e.Type)).
					Uint64("superblock_number", e.SuperblockNumber).
					Str("superblock_hash", common.BytesToHash(e.SuperblockHash).Hex()).
					Uint64("l1_block_number", e.L1BlockNumber).
					Str("l1_tx_hash", common.BytesToHash(e.L1TransactionHash).Hex()).
					Msg("Received superblock event")

				out <- e
			}
		}
	}()
	return out, nil
}

// GetLatestL1Block returns basic head info.
func (p *EthPublisher) GetLatestL1Block(ctx context.Context) (*BlockInfo, error) {
	head, err := p.client.HeaderByNumber(ctx, nil)
	if err != nil {
		p.log.Error().Err(err).Msg("Failed to get latest L1 block header")
		return nil, err
	}

	p.log.Debug().
		Uint64("block_number", head.Number.Uint64()).
		Str("block_hash", head.Hash().Hex()).
		Uint64("timestamp", head.Time).
		Msg("Retrieved latest L1 block")
	bi := &BlockInfo{
		Number:     head.Number.Uint64(),
		Hash:       head.Hash().Bytes(),
		ParentHash: head.ParentHash.Bytes(),
		Timestamp:  time.Unix(int64(head.Time), 0),
		GasLimit:   head.GasLimit,
		GasUsed:    head.GasUsed,
	}
	return bi, nil
}

// cryptoToECDSA parses a raw 32-byte private key into ecdsa.PrivateKey.
// Defined here to avoid importing crypto directly in tests.
func cryptoToECDSA(priv []byte) (*ecdsa.PrivateKey, error) {
	return crypto.ToECDSA(priv)
}
