package contracts

import (
	"context"
	_ "embed"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ssvlabs/rollup-shared-publisher/x/superblock/store"
)

//go:embed abi/l2_output_oracle.json
var l2OutputOracleABIJSON string

// L2OutputOracleBinding encodes proposeL2Output(SuperBlock) calls.
type L2OutputOracleBinding struct {
	address common.Address
	abi     abi.ABI
}

func NewL2OutputOracleBinding(contractAddr string) (*L2OutputOracleBinding, error) {
	if strings.TrimSpace(contractAddr) == "" {
		return nil, fmt.Errorf("contract address is empty")
	}
	a, err := abi.JSON(strings.NewReader(l2OutputOracleABIJSON))
	if err != nil {
		return nil, fmt.Errorf("parse ABI: %w", err)
	}
	return &L2OutputOracleBinding{address: common.HexToAddress(contractAddr), abi: a}, nil
}

func (b *L2OutputOracleBinding) Address() common.Address { return b.address }

func (b *L2OutputOracleBinding) ABI() abi.ABI { return b.abi }

type l2BlockArg struct {
	Slot            uint64   `abi:"slot"`
	ChainId         []byte   `abi:"chainId"`
	BlockNumber     uint64   `abi:"blockNumber"`
	BlockHash       []byte   `abi:"blockHash"`
	ParentBlockHash []byte   `abi:"parentBlockHash"`
	IncludedXts     [][]byte `abi:"included_xts"`
	Block           []byte   `abi:"block"`
}

type superBlockArg struct {
	BlockNumber       uint64       `abi:"blockNumber"`
	Slot              uint64       `abi:"slot"`
	ParentBlockHash   []byte       `abi:"parentBlockHash"`
	MerkleRoot        []byte       `abi:"merkleRoot"`
	Timestamp         *big.Int     `abi:"timestamp"`
	L2Blocks          []l2BlockArg `abi:"l2Blocks"`
	IncludedXTs       [][]byte     `abi:"_includedXTs"`
	TransactionHashes []byte       `abi:"_transactionHashes"`
	Status            uint8        `abi:"status"`
}

func (b *L2OutputOracleBinding) BuildPublishCalldata(_ context.Context, sb *store.Superblock) ([]byte, error) {
	if sb == nil {
		return nil, fmt.Errorf("superblock is nil")
	}

	// Map L2 blocks
	l2s := make([]l2BlockArg, 0, len(sb.L2Blocks))
	for _, blk := range sb.L2Blocks {
		if blk == nil {
			continue
		}
		l2s = append(l2s, l2BlockArg{
			Slot:            blk.Slot,
			ChainId:         blk.ChainId,
			BlockNumber:     blk.BlockNumber,
			BlockHash:       blk.BlockHash,
			ParentBlockHash: blk.ParentBlockHash,
			IncludedXts:     blk.IncludedXts,
			Block:           blk.Block,
		})
	}

	// Transaction hashes: pack IncludedXTs sequentially as 32-byte blobs
	txHashes := packTxHashes(sb.IncludedXTs)
	status := mapStatus(sb.Status)

	arg := superBlockArg{
		BlockNumber:       sb.Number,
		Slot:              sb.Slot,
		ParentBlockHash:   sb.ParentHash,
		MerkleRoot:        sb.MerkleRoot,
		Timestamp:         big.NewInt(sb.Timestamp.Unix()),
		L2Blocks:          l2s,
		IncludedXTs:       sb.IncludedXTs,
		TransactionHashes: txHashes,
		Status:            status,
	}

	data, err := b.abi.Pack("proposeL2Output", arg)
	if err != nil {
		return nil, fmt.Errorf("abi pack proposeL2Output: %w", err)
	}
	return data, nil
}

func packTxHashes(included [][]byte) []byte {
	if len(included) == 0 {
		return nil
	}
	// Flatten into single bytes buffer (right-padded/trimmed to 32 each)
	out := make([]byte, 0, 32*len(included))
	for _, h := range included {
		// ensure 32 bytes by left-padding if shorter, truncating if longer
		var buf [32]byte
		if len(h) >= 32 {
			copy(buf[:], h[len(h)-32:])
		} else {
			copy(buf[32-len(h):], h)
		}
		out = append(out, buf[:]...)
	}
	return out
}

func mapStatus(s store.SuperblockStatus) uint8 {
	switch s {
	case store.SuperblockStatusSubmitted:
		return 1
	case store.SuperblockStatusConfirmed:
		return 2
	case store.SuperblockStatusFinalized:
		return 3
	case store.SuperblockStatusRolledBack:
		return 4
	case store.SuperblockStatusPending:
		fallthrough
	default:
		return 0
	}
}
