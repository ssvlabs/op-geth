package main

import (
	"crypto/ecdsa"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// MyToken = 0x6d19CB7639DeB366c334BD69f030A38e226BA6d2

// == Logs ==
//   Mailbox:   0x6e3f6C93C58f6E3523D18930a58c0FFAc13833aB
//   PingPong:   0x0C151ff329d0D21D0481933b2f524554b116C862
//   Coordinator:   0x0f10aF865F68F5aA1dDB7c5b5A1a0f396232C6Be
//   Bridge:   0xB37E43fd6C32e4d0E37FA38F8c7F00d0a72ddA1B
//
//   chain id 11111
//
// == Logs ==
//   Mailbox:   0xa4EEBE333Bc9b707198297ea23B77fB104A8e443
//   PingPong:   0x6e3f6C93C58f6E3523D18930a58c0FFAc13833aB
//   Coordinator:   0x0f10aF865F68F5aA1dDB7c5b5A1a0f396232C6Be
//   Bridge:   0x0C151ff329d0D21D0481933b2f524554b116C862
//
// ## Setting up 1 EVM.
//
// ==========================
//
// Chain 22222

const (
	bridgeABI = `[{"type":"constructor","inputs":[{"name":"_mailbox","type":"address","internalType":"address"}],"stateMutability":"nonpayable"},{"type":"function","name":"checkAck","inputs":[{"name":"chainDest","type":"uint256","internalType":"uint256"},{"name":"destBridge","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"}],"outputs":[{"name":"","type":"bytes","internalType":"bytes"}],"stateMutability":"view"},{"type":"function","name":"mailbox","inputs":[],"outputs":[{"name":"","type":"address","internalType":"contract IMailbox"}],"stateMutability":"view"},{"type":"function","name":"receiveTokens","inputs":[{"name":"otherChainId","type":"uint256","internalType":"uint256"},{"name":"sender","type":"address","internalType":"address"},{"name":"receiver","type":"address","internalType":"address"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"srcBridge","type":"address","internalType":"address"}],"outputs":[{"name":"token","type":"address","internalType":"address"},{"name":"amount","type":"uint256","internalType":"uint256"}],"stateMutability":"nonpayable"},{"type":"function","name":"send","inputs":[{"name":"otherChainId","type":"uint256","internalType":"uint256"},{"name":"token","type":"address","internalType":"address"},{"name":"sender","type":"address","internalType":"address"},{"name":"receiver","type":"address","internalType":"address"},{"name":"amount","type":"uint256","internalType":"uint256"},{"name":"sessionId","type":"uint256","internalType":"uint256"},{"name":"destBridge","type":"address","internalType":"address"}],"outputs":[],"stateMutability":"nonpayable"},{"type":"event","name":"DataWritten","inputs":[{"name":"data","type":"bytes","indexed":false,"internalType":"bytes"}],"anonymous":false},{"type":"event","name":"TokensReceived","inputs":[{"name":"token","type":"address","indexed":false,"internalType":"address"},{"name":"amount","type":"uint256","indexed":false,"internalType":"uint256"}],"anonymous":false}]`
)

type BridgeParams struct {
	ChainSrc   *big.Int
	ChainDest  *big.Int
	Token      common.Address
	Sender     common.Address
	Receiver   common.Address
	Amount     *big.Int
	SessionId  *big.Int
	DestBridge common.Address
	SrcBridge  common.Address
}

func createSendTransaction(params BridgeParams, nonce uint64, privateKey *ecdsa.PrivateKey, bridgeAddress common.Address) (*types.Transaction, error) {
	parsedABI, err := abi.JSON(strings.NewReader(bridgeABI))
	if err != nil {
		return nil, err
	}

	calldata, err := parsedABI.Pack("send",
		params.ChainDest,
		params.Token,
		params.Sender,
		params.Receiver,
		params.Amount,
		params.SessionId,
		params.DestBridge,
	)
	if err != nil {
		return nil, err
	}

	contract := bridgeAddress
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

func createReceiveTransaction(params BridgeParams, nonce uint64, privateKey *ecdsa.PrivateKey, bridgeAddress common.Address) (*types.Transaction, error) {
	parsedABI, err := abi.JSON(strings.NewReader(bridgeABI))
	if err != nil {
		return nil, err
	}

	calldata, err := parsedABI.Pack("receiveTokens",
		params.ChainSrc,
		params.Sender,
		params.Receiver,
		params.SessionId,
		params.SrcBridge,
	)
	if err != nil {
		return nil, err
	}

	contract := bridgeAddress
	txData := &types.DynamicFeeTx{
		ChainID:    params.ChainDest,
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
	return types.SignTx(tx, types.NewLondonSigner(params.ChainDest), privateKey)
}
