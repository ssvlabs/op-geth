package store

import (
	"context"

	pb "github.com/ssvlabs/rollup-shared-publisher/proto/rollup/v1"
)

type L2BlockStore interface {
	StoreL2Block(ctx context.Context, block *pb.L2Block) error
	GetL2Block(ctx context.Context, chainID []byte, blockNumber uint64) (*pb.L2Block, error)
	GetL2BlockByHash(ctx context.Context, blockHash []byte) (*pb.L2Block, error)
	GetLatestL2Block(ctx context.Context, chainID []byte) (*pb.L2Block, error)
	GetL2BlocksForSlot(ctx context.Context, slot uint64) ([]*pb.L2Block, error)
	DeleteL2BlocksBeforeSlot(ctx context.Context, slot uint64) error
}

type SuperblockStore interface {
	StoreSuperblock(ctx context.Context, superblock *Superblock) error
	GetSuperblock(ctx context.Context, number uint64) (*Superblock, error)
	GetSuperblockByHash(ctx context.Context, hash []byte) (*Superblock, error)
	GetLatestSuperblock(ctx context.Context) (*Superblock, error)
	GetSuperblockCount(ctx context.Context) (uint64, error)
	DeleteSuperblock(ctx context.Context, number uint64) error
}
