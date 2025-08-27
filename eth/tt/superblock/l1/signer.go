package l1

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// Signer abstracts transaction signing for the publisher.
type Signer interface {
	From() common.Address
	ChainID(ctx context.Context) (*big.Int, error)
	SignTx(ctx context.Context, tx *types.Transaction) (*types.Transaction, error)
}

// LocalECDSASigner signs transactions with a local secp256k1 private key.
type LocalECDSASigner struct {
	chainID *big.Int
	key     *ecdsa.PrivateKey
	from    common.Address
}

func NewLocalECDSASigner(chainID *big.Int, key *ecdsa.PrivateKey) *LocalECDSASigner {
	return &LocalECDSASigner{
		chainID: chainID,
		key:     key,
		from:    crypto.PubkeyToAddress(key.PublicKey),
	}
}

func (s *LocalECDSASigner) From() common.Address { return s.from }

func (s *LocalECDSASigner) ChainID(_ context.Context) (*big.Int, error) {
	if s.chainID == nil {
		return nil, fmt.Errorf("signer chainID not set")
	}
	return new(big.Int).Set(s.chainID), nil
}

func (s *LocalECDSASigner) SignTx(_ context.Context, tx *types.Transaction) (*types.Transaction, error) {
	return types.SignTx(tx, types.LatestSignerForChainID(s.chainID), s.key)
}
