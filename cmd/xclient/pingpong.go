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
	pingPongAddr = "0x8c113C07901a86A34ddbAeAE811eBc41e8c64067"
	pingPongABI  = `[{"type":"constructor","inputs":[{"name":"_mailbox","type":"address","internalType":"address"}],"stateMutability":"nonpayable"},{"type":"function","name":"mailbox","inputs":[],"outputs":[{"name":"","type":"address","internalType":"contract IMailbox"}],"stateMutability":"view"},{"type":"function","name":"ping","inputs":[{"name":"chainSrc","type":"uint256","internalType":"uint256"},{"name":"chainDest","type":"uint256","internalType":"uint256"},{"name":"sender","type":"address","internalType":"address"},{"name":"receiver","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"data","type":"bytes","internalType":"bytes"}],"outputs":[{"name":"pongMessage","type":"bytes","internalType":"bytes"}],"stateMutability":"nonpayable"},{"type":"function","name":"pong","inputs":[{"name":"chainSrc","type":"uint256","internalType":"uint256"},{"name":"chainDest","type":"uint256","internalType":"uint256"},{"name":"sender","type":"address","internalType":"address"},{"name":"receiver","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"data","type":"bytes","internalType":"bytes"}],"outputs":[{"name":"pingMessage","type":"bytes","internalType":"bytes"}],"stateMutability":"nonpayable"}]`
)

type PingPongParams struct {
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

	contract := common.HexToAddress(pingPongAddr)
	txData := &types.DynamicFeeTx{
		ChainID:    params.ChainSrc,
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
	return types.SignTx(tx, types.NewLondonSigner(params.ChainSrc), privateKey)
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

	contract := common.HexToAddress(pingPongAddr)
	txData := &types.DynamicFeeTx{
		ChainID:    params.ChainSrc,
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
	return types.SignTx(tx, types.NewLondonSigner(params.ChainSrc), privateKey)
}
