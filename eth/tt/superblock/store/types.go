package store

import (
	"time"

	pb "github.com/ssvlabs/rollup-shared-publisher/proto/rollup/v1"
)

type Superblock struct {
	Number            uint64           `json:"number"`
	Slot              uint64           `json:"slot"`
	ParentHash        []byte           `json:"parent_hash"`
	Hash              []byte           `json:"hash"`
	MerkleRoot        []byte           `json:"merkle_root"`
	Timestamp         time.Time        `json:"timestamp"`
	L2Blocks          []*pb.L2Block    `json:"l2_blocks"`
	IncludedXTs       [][]byte         `json:"included_xts"`
	L1TransactionHash []byte           `json:"l1_transaction_hash,omitempty"`
	Status            SuperblockStatus `json:"status"`
}

type SuperblockStatus string

const (
	SuperblockStatusPending    SuperblockStatus = "pending"
	SuperblockStatusSubmitted  SuperblockStatus = "submitted"
	SuperblockStatusConfirmed  SuperblockStatus = "confirmed"
	SuperblockStatusFinalized  SuperblockStatus = "finalized"
	SuperblockStatusRolledBack SuperblockStatus = "rolled_back"
)
