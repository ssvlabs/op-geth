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
	pingPongAddr = "0x5217C9034048B1Fa9Fb1e300F94fCd7002138Ea5"
	pingPongABI  = `[{"inputs":[{"internalType":"address","name":"_mailbox","type":"address"}],"stateMutability":"nonpayable","type":"constructor"},{"inputs":[],"name":"mailbox","outputs":[{"internalType":"contract IMailbox","name":"","type":"address"}],"stateMutability":"view","type":"function"},{"inputs":[{"internalType":"uint256","name":"chainSrc","type":"uint256"},{"internalType":"uint256","name":"chainDest","type":"uint256"},{"internalType":"address","name":"sender","type":"address"},{"internalType":"address","name":"receiver","type":"address"},{"internalType":"uint256","name":"sessionId","type":"uint256"},{"internalType":"bytes","name":"data","type":"bytes"},{"internalType":"bytes","name":"label","type":"bytes"}],"name":"ping","outputs":[{"internalType":"bytes","name":"pongMessage","type":"bytes"}],"stateMutability":"nonpayable","type":"function"},{"inputs":[{"internalType":"uint256","name":"chainSrc","type":"uint256"},{"internalType":"uint256","name":"chainDest","type":"uint256"},{"internalType":"address","name":"sender","type":"address"},{"internalType":"address","name":"receiver","type":"address"},{"internalType":"uint256","name":"sessionId","type":"uint256"},{"internalType":"bytes","name":"data","type":"bytes"},{"internalType":"bytes","name":"label","type":"bytes"}],"name":"pong","outputs":[{"internalType":"bytes","name":"pingMessage","type":"bytes"}],"stateMutability":"nonpayable","type":"function"}]`
)

type PingPongParams struct {
	ChainSrc  *big.Int
	ChainDest *big.Int
	Sender    common.Address
	Receiver  common.Address
	SessionId *big.Int
	Data      []byte
	Label     []byte
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
		params.Label,
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
		params.Label,
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
