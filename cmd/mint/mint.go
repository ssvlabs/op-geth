package main

import (
	"crypto/ecdsa"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

const tokenABI = `[{"inputs":[{"internalType":"address","name":"account","type":"address"},{"internalType":"uint256","name":"value","type":"uint256"}],"name":"mint","outputs":[],"stateMutability":"nonpayable","type":"function"}]`

type MintParams struct {
	ChainSrc *big.Int
	Token    common.Address
	Receiver common.Address
	Amount   *big.Int
}

func createMintTransaction(params MintParams, nonce uint64, privateKey *ecdsa.PrivateKey) (*types.Transaction, error) {
	parsedABI, err := abi.JSON(strings.NewReader(tokenABI))
	if err != nil {
		return nil, err
	}

	calldata, err := parsedABI.Pack("mint", params.Receiver, params.Amount)
	if err != nil {
		return nil, err
	}

	token := params.Token
	txData := &types.DynamicFeeTx{
		ChainID:    params.ChainSrc,
		Nonce:      nonce,
		GasTipCap:  big.NewInt(1_000_000_000),
		GasFeeCap:  big.NewInt(20_000_000_000),
		Gas:        300000,
		To:         &token,
		Value:      big.NewInt(0),
		Data:       calldata,
		AccessList: nil,
	}

	tx := types.NewTx(txData)
	return types.SignTx(tx, types.NewLondonSigner(params.ChainSrc), privateKey)
}
