package main

import (
	"crypto/ecdsa"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

const (
	pingPongAddr = "0x1a8211F40C3E437Ec49911e705263C2b12b5C5Fd"
	pingPongABI  = `[{"type":"constructor","inputs":[{"name":"_mailbox","type":"address","internalType":"address"}],"stateMutability":"nonpayable"},{"type":"function","name":"mailbox","inputs":[],"outputs":[{"name":"","type":"address","internalType":"contract IMailbox"}],"stateMutability":"view"},{"type":"function","name":"ping","inputs":[{"name":"otherChain","type":"uint256","internalType":"uint256"},{"name":"pongSender","type":"address","internalType":"address"},{"name":"pingReceiver","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"data","type":"bytes","internalType":"bytes"}],"outputs":[{"name":"pongMessage","type":"bytes","internalType":"bytes"}],"stateMutability":"nonpayable"},{"type":"function","name":"pong","inputs":[{"name":"otherChain","type":"uint256","internalType":"uint256"},{"name":"pingSender","type":"address","internalType":"address"},{"name":"pongReceiver","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"data","type":"bytes","internalType":"bytes"}],"outputs":[{"name":"pingMessage","type":"bytes","internalType":"bytes"}],"stateMutability":"nonpayable"},{"type":"error","name":"PingMessageEmpty","inputs":[]},{"type":"error","name":"PongMessageEmpty","inputs":[]}]`
)

type PingPongParams struct {
	TxChainID *big.Int       // Chain ID for transaction signing
	ChainSrc  *big.Int       // Source chain (not used in ping/pong calls directly)
	ChainDest *big.Int       // Destination chain (used as otherChain parameter)
	Sender    common.Address // Expected sender of the other message
	Receiver  common.Address // Receiver on the other chain
	SessionId *big.Int
	Data      []byte
}

func createPingTransaction(params PingPongParams, nonce uint64, privateKey *ecdsa.PrivateKey) (*types.Transaction, error) {
	parsedABI, err := abi.JSON(strings.NewReader(pingPongABI))
	if err != nil {
		return nil, err
	}

	calldata, err := parsedABI.Pack("ping",
		params.ChainDest, // otherChain (destination chain)
		params.Sender,    // pongSender (expected sender of PONG message)
		params.Receiver,  // pingReceiver (receiver on destination chain)
		params.SessionId, // sessionId
		params.Data,      // data
	)
	if err != nil {
		return nil, err
	}

	contract := common.HexToAddress(pingPongAddr)
	txData := &types.DynamicFeeTx{
		ChainID:    params.TxChainID,
		Nonce:      nonce,
		GasTipCap:  big.NewInt(1000000000),
		GasFeeCap:  big.NewInt(20000000000),
		Gas:        300000,
		To:         &contract,
		Value:      big.NewInt(0),
		Data:       calldata,
		AccessList: nil,
	}

	tx := types.NewTx(txData)
	return types.SignTx(tx, types.NewLondonSigner(params.TxChainID), privateKey)
}

func createPongTransaction(params PingPongParams, nonce uint64, privateKey *ecdsa.PrivateKey) (*types.Transaction, error) {
	parsedABI, err := abi.JSON(strings.NewReader(pingPongABI))
	if err != nil {
		return nil, err
	}

	calldata, err := parsedABI.Pack("pong",
		params.ChainSrc,  // otherChain (source chain where PING came from)
		params.Sender,    // pingSender (expected sender of PING message)
		params.Receiver,  // pongReceiver (receiver on source chain)
		params.SessionId, // sessionId
		params.Data,      // data
	)
	if err != nil {
		return nil, err
	}

	contract := common.HexToAddress(pingPongAddr)
	txData := &types.DynamicFeeTx{
		ChainID:    params.TxChainID,
		Nonce:      nonce,
		GasTipCap:  big.NewInt(1000000000),
		GasFeeCap:  big.NewInt(20000000000),
		Gas:        300000,
		To:         &contract,
		Value:      big.NewInt(0),
		Data:       calldata,
		AccessList: nil,
	}

	tx := types.NewTx(txData)
	return types.SignTx(tx, types.NewLondonSigner(params.TxChainID), privateKey)
}
