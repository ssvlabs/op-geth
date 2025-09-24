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
	pingPongAddrA = "0x1a8211F40C3E437Ec49911e705263C2b12b5C5Fd"
	pingPongAddrB = "0x1a8211F40C3E437Ec49911e705263C2b12b5C5Fd"
	pingPongABI   = `[{"type":"constructor","inputs":[{"name":"_mailbox","type":"address","internalType":"address"}],"stateMutability":"nonpayable"},{"type":"function","name":"mailbox","inputs":[],"outputs":[{"name":"","type":"address","internalType":"contract IMailbox"}],"stateMutability":"view"},{"type":"function","name":"ping","inputs":[{"name":"otherChain","type":"uint256","internalType":"uint256"},{"name":"pongSender","type":"address","internalType":"address"},{"name":"pingReceiver","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"data","type":"bytes","internalType":"bytes"}],"outputs":[{"name":"pongMessage","type":"bytes","internalType":"bytes"}],"stateMutability":"nonpayable"},{"type":"function","name":"pong","inputs":[{"name":"otherChain","type":"uint256","internalType":"uint256"},{"name":"pingSender","type":"address","internalType":"address"},{"name":"pongReceiver","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"data","type":"bytes","internalType":"bytes"}],"outputs":[{"name":"pingMessage","type":"bytes","internalType":"bytes"}],"stateMutability":"nonpayable"},{"type":"error","name":"PingMessageEmpty","inputs":[]},{"type":"error","name":"PongMessageEmpty","inputs":[]}]`
)

type PingPongParams struct {
	TxChainID *big.Int
	ChainSrc  *big.Int
	ChainDest *big.Int
	Sender    common.Address
	Receiver  common.Address
	SessionId *big.Int
	Data      []byte
}

func createPingTransaction(params PingPongParams, nonce uint64, privateKey *ecdsa.PrivateKey) (*types.Transaction, error) {
	parsedABI, err := abi.JSON(strings.NewReader(pingPongABI))
	if err != nil {
		return nil, err
	}

	calldata, err := parsedABI.Pack("ping",
		params.ChainSrc,
		params.ChainDest,
		params.Sender,
		params.Receiver,
		params.SessionId,
		params.Data,
	)
	if err != nil {
		return nil, err
	}

	contract := common.HexToAddress(pingPongAddrA)
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
		params.ChainSrc,
		params.ChainDest,
		params.Sender,
		params.Receiver,
		params.SessionId,
		params.Data,
	)
	if err != nil {
		return nil, err
	}

	contract := common.HexToAddress(pingPongAddrB)
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
