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
	tokenAddrA = "0x2a72a18AB5293C7D436153EdD3F8c5b8FEBBA4F3"
	tokenAddrB = "0x2a72a18AB5293C7D436153EdD3F8c5b8FEBBA4F3"
	tokenABI   = `[{"type":"constructor","inputs":[],"stateMutability":"nonpayable"},{"type":"function","name":"allowance","inputs":[{"name":"owner","type":"address","internalType":"address"},{"name":"spender","type":"address","internalType":"address"}],"outputs":[{"name":"","type":"uint256","internalType":"uint256"}],"stateMutability":"view"},{"type":"function","name":"approve","inputs":[{"name":"spender","type":"address","internalType":"address"},{"name":"value","type":"uint256","internalType":"uint256"}],"outputs":[{"name":"","type":"bool","internalType":"bool"}],"stateMutability":"nonpayable"},{"type":"function","name":"balanceOf","inputs":[{"name":"account","type":"address","internalType":"address"}],"outputs":[{"name":"","type":"uint256","internalType":"uint256"}],"stateMutability":"view"},{"type":"function","name":"burn","inputs":[{"name":"account","type":"address","internalType":"address"},{"name":"value","type":"uint256","internalType":"uint256"}],"outputs":[],"stateMutability":"nonpayable"},{"type":"function","name":"decimals","inputs":[],"outputs":[{"name":"","type":"uint8","internalType":"uint8"}],"stateMutability":"view"},{"type":"function","name":"mint","inputs":[{"name":"account","type":"address","internalType":"address"},{"name":"value","type":"uint256","internalType":"uint256"}],"outputs":[],"stateMutability":"nonpayable"},{"type":"function","name":"name","inputs":[],"outputs":[{"name":"","type":"string","internalType":"string"}],"stateMutability":"view"},{"type":"function","name":"symbol","inputs":[],"outputs":[{"name":"","type":"string","internalType":"string"}],"stateMutability":"view"},{"type":"function","name":"totalSupply","inputs":[],"outputs":[{"name":"","type":"uint256","internalType":"uint256"}],"stateMutability":"view"},{"type":"function","name":"transfer","inputs":[{"name":"to","type":"address","internalType":"address"},{"name":"value","type":"uint256","internalType":"uint256"}],"outputs":[{"name":"","type":"bool","internalType":"bool"}],"stateMutability":"nonpayable"},{"type":"function","name":"transferFrom","inputs":[{"name":"from","type":"address","internalType":"address"},{"name":"to","type":"address","internalType":"address"},{"name":"value","type":"uint256","internalType":"uint256"}],"outputs":[{"name":"","type":"bool","internalType":"bool"}],"stateMutability":"nonpayable"},{"type":"event","name":"Approval","inputs":[{"name":"owner","type":"address","indexed":true,"internalType":"address"},{"name":"spender","type":"address","indexed":true,"internalType":"address"},{"name":"value","type":"uint256","indexed":false,"internalType":"uint256"}],"anonymous":false},{"type":"event","name":"Transfer","inputs":[{"name":"from","type":"address","indexed":true,"internalType":"address"},{"name":"to","type":"address","indexed":true,"internalType":"address"},{"name":"value","type":"uint256","indexed":false,"internalType":"uint256"}],"anonymous":false},{"type":"error","name":"ERC20InsufficientAllowance","inputs":[{"name":"spender","type":"address","internalType":"address"},{"name":"allowance","type":"uint256","internalType":"uint256"},{"name":"needed","type":"uint256","internalType":"uint256"}]},{"type":"error","name":"ERC20InsufficientBalance","inputs":[{"name":"sender","type":"address","internalType":"address"},{"name":"balance","type":"uint256","internalType":"uint256"},{"name":"needed","type":"uint256","internalType":"uint256"}]},{"type":"error","name":"ERC20InvalidApprover","inputs":[{"name":"approver","type":"address","internalType":"address"}]},{"type":"error","name":"ERC20InvalidReceiver","inputs":[{"name":"receiver","type":"address","internalType":"address"}]},{"type":"error","name":"ERC20InvalidSender","inputs":[{"name":"sender","type":"address","internalType":"address"}]},{"type":"error","name":"ERC20InvalidSpender","inputs":[{"name":"spender","type":"address","internalType":"address"}]},{"type":"error","name":"Unauthorized","inputs":[]}]`
)

type MintParams struct {
	ChainSrc *big.Int

	Receiver common.Address
	Amount   *big.Int
}

func createMintTransaction(params MintParams, nonce uint64, privateKey *ecdsa.PrivateKey) (*types.Transaction, error) {
	parsedABI, err := abi.JSON(strings.NewReader(tokenABI))
	if err != nil {
		return nil, err
	}

	calldata, err := parsedABI.Pack("mint",
		params.Receiver,
		params.Amount,
	)
	if err != nil {
		return nil, err
	}

	contract := common.HexToAddress(tokenAddrA)
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
