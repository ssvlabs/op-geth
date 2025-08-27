package events

import (
	"context"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
)

// LogClient defines the subset of client methods used by the watcher.
type LogClient interface {
	SubscribeFilterLogs(ctx context.Context, q ethereum.FilterQuery, ch chan<- types.Log) (ethereum.Subscription, error)
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
}

// SuperblockEvent represents a superblock-related event from L1.
type SuperblockEvent struct {
	Type              SuperblockEventType `json:"type"`
	SuperblockNumber  uint64              `json:"superblock_number"`
	SuperblockHash    []byte              `json:"superblock_hash"`
	L1BlockNumber     uint64              `json:"l1_block_number"`
	L1TransactionHash []byte              `json:"l1_transaction_hash"`
	Removed           bool                `json:"removed"`
	Timestamp         time.Time           `json:"timestamp"`
}

type SuperblockEventType string

const (
	SuperblockEventSubmitted  SuperblockEventType = "submitted"
	SuperblockEventConfirmed  SuperblockEventType = "confirmed"
	SuperblockEventRolledBack SuperblockEventType = "rolled_back"
)

// Internal tuple types for ABI unpacking
type l2BlockTuple struct {
	Slot            uint64
	ChainId         []byte
	BlockNumber     uint64
	BlockHash       []byte
	ParentBlockHash []byte
	IncludedXts     [][]byte `abi:"included_xts"`
	Block           []byte
}

type superBlockTuple struct {
	BlockNumber       uint64
	Slot              uint64
	ParentBlockHash   []byte
	MerkleRoot        []byte
	Timestamp         *big.Int
	L2Blocks          []l2BlockTuple
	IncludedXTs       [][]byte `abi:"_includedXTs"`
	TransactionHashes []byte   `abi:"_transactionHashes"`
	Status            uint8
}
