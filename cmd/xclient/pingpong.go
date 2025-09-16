package main

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

const (
	pingPongAddrA  = "0x97487E10f0E0302718291d1Fd6aEF040Bc87bb38"
	pingPongAddrB  = "0xD044e7ABad4662f2243A6eF5636973Ec80EF7551"
	rollupAChainID = uint64(77777)
	rollupBChainID = uint64(88888)
	pingPongABI    = `[{"type":"constructor","inputs":[{"name":"_mailbox","type":"address","internalType":"address"}],"stateMutability":"nonpayable"},{"type":"function","name":"mailbox","inputs":[],"outputs":[{"name":"","type":"address","internalType":"contract IMailbox"}],"stateMutability":"view"},{"type":"function","name":"ping","inputs":[{"name":"chainSrc","type":"uint256","internalType":"uint256"},{"name":"chainDest","type":"uint256","internalType":"uint256"},{"name":"sender","type":"address","internalType":"address"},{"name":"receiver","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"data","type":"bytes","internalType":"bytes"}],"outputs":[{"name":"pongMessage","type":"bytes","internalType":"bytes"}],"stateMutability":"nonpayable"},{"type":"function","name":"pong","inputs":[{"name":"chainSrc","type":"uint256","internalType":"uint256"},{"name":"chainDest","type":"uint256","internalType":"uint256"},{"name":"sender","type":"address","internalType":"address"},{"name":"receiver","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"data","type":"bytes","internalType":"bytes"}],"outputs":[{"name":"pingMessage","type":"bytes","internalType":"bytes"}],"stateMutability":"nonpayable"}]`
)

type PingPongParams struct {
	ExecChainID *big.Int
	ChainSrc    *big.Int
	ChainDest   *big.Int
	Sender      common.Address
	Receiver    common.Address
	SessionId   *big.Int
	Data        []byte
}

func pingPongAddress(chainID *big.Int) (common.Address, error) {
	switch chainID.Uint64() {
	case rollupAChainID:
		return common.HexToAddress(pingPongAddrA), nil
	case rollupBChainID:
		return common.HexToAddress(pingPongAddrB), nil
	default:
		return common.Address{}, fmt.Errorf("unsupported chain id %s for pingpong contract", chainID.String())
	}
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

	contractAddr, err := pingPongAddress(params.ExecChainID)
	if err != nil {
		return nil, err
	}

	contract := contractAddr
	txData := &types.DynamicFeeTx{
		ChainID:    params.ExecChainID,
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
	return types.SignTx(tx, types.NewLondonSigner(params.ExecChainID), privateKey)
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

	contractAddr, err := pingPongAddress(params.ExecChainID)
	if err != nil {
		return nil, err
	}

	contract := contractAddr
	txData := &types.DynamicFeeTx{
		ChainID:    params.ExecChainID,
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
	return types.SignTx(tx, types.NewLondonSigner(params.ExecChainID), privateKey)
}
