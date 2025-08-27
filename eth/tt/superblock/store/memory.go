// Package store. This memory store is for testing purposes only. True production store will be implemented later.
// So im adding a TODO comment here.
package store

import (
	"context"
	"fmt"
	"sync"

	pb "github.com/ssvlabs/rollup-shared-publisher/proto/rollup/v1"
)

// MemoryL2BlockStore provides in-memory storage for L2 blocks.
type MemoryL2BlockStore struct {
	mu     sync.RWMutex
	blocks map[string]*pb.L2Block // key: chainID_blockNumber
	latest map[string]*pb.L2Block // key: chainID -> latest block
}

func NewMemoryL2BlockStore() *MemoryL2BlockStore {
	return &MemoryL2BlockStore{
		blocks: make(map[string]*pb.L2Block),
		latest: make(map[string]*pb.L2Block),
	}
}

func (s *MemoryL2BlockStore) StoreL2Block(_ context.Context, block *pb.L2Block) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	chainID := string(block.ChainId)
	key := fmt.Sprintf("%s_%d", chainID, block.BlockNumber)

	s.blocks[key] = block

	if latest, exists := s.latest[chainID]; !exists || block.BlockNumber > latest.BlockNumber {
		s.latest[chainID] = block
	}

	return nil
}

func (s *MemoryL2BlockStore) GetL2Block(_ context.Context, chainID []byte, blockNumber uint64) (*pb.L2Block, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := fmt.Sprintf("%s_%d", string(chainID), blockNumber)
	if block, exists := s.blocks[key]; exists {
		return block, nil
	}
	return nil, fmt.Errorf("block not found: chain=%x, number=%d", chainID, blockNumber)
}

func (s *MemoryL2BlockStore) GetL2BlockByHash(_ context.Context, blockHash []byte) (*pb.L2Block, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, block := range s.blocks {
		if string(block.BlockHash) == string(blockHash) {
			return block, nil
		}
	}
	return nil, fmt.Errorf("block not found: hash=%x", blockHash)
}

func (s *MemoryL2BlockStore) GetLatestL2Block(_ context.Context, chainID []byte) (*pb.L2Block, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if block, exists := s.latest[string(chainID)]; exists {
		return block, nil
	}
	return nil, fmt.Errorf("no blocks found for chain: %x", chainID)
}

func (s *MemoryL2BlockStore) GetL2BlocksForSlot(_ context.Context, slot uint64) ([]*pb.L2Block, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*pb.L2Block
	for _, block := range s.blocks {
		if block.Slot == slot {
			result = append(result, block)
		}
	}
	return result, nil
}

func (s *MemoryL2BlockStore) DeleteL2BlocksBeforeSlot(_ context.Context, slot uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for key, block := range s.blocks {
		if block.Slot < slot {
			delete(s.blocks, key)
		}
	}
	return nil
}

// MemorySuperblockStore provides in-memory storage for superblocks.
type MemorySuperblockStore struct {
	mu          sync.RWMutex
	superblocks map[uint64]*Superblock // key: superblock number
	byHash      map[string]*Superblock // key: hex hash
	latest      *Superblock
}

func NewMemorySuperblockStore() *MemorySuperblockStore {
	return &MemorySuperblockStore{
		superblocks: make(map[uint64]*Superblock),
		byHash:      make(map[string]*Superblock),
	}
}

func (s *MemorySuperblockStore) StoreSuperblock(_ context.Context, superblock *Superblock) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.superblocks[superblock.Number] = superblock
	s.byHash[fmt.Sprintf("%x", superblock.Hash)] = superblock

	if s.latest == nil || superblock.Number > s.latest.Number {
		s.latest = superblock
	}

	return nil
}

func (s *MemorySuperblockStore) GetSuperblock(_ context.Context, number uint64) (*Superblock, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if superblock, exists := s.superblocks[number]; exists {
		return superblock, nil
	}
	return nil, fmt.Errorf("superblock not found: %d", number)
}

func (s *MemorySuperblockStore) GetSuperblockByHash(_ context.Context, hash []byte) (*Superblock, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	hashStr := fmt.Sprintf("%x", hash)
	if superblock, exists := s.byHash[hashStr]; exists {
		return superblock, nil
	}
	return nil, fmt.Errorf("superblock not found: %x", hash)
}

func (s *MemorySuperblockStore) GetLatestSuperblock(context.Context) (*Superblock, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.latest == nil {
		return nil, fmt.Errorf("no superblocks found")
	}
	return s.latest, nil
}

func (s *MemorySuperblockStore) GetSuperblockCount(context.Context) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return uint64(len(s.superblocks)), nil
}

func (s *MemorySuperblockStore) DeleteSuperblock(_ context.Context, number uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if superblock, exists := s.superblocks[number]; exists {
		delete(s.superblocks, number)
		delete(s.byHash, fmt.Sprintf("%x", superblock.Hash))

		if s.latest != nil && s.latest.Number == number {
			s.latest = nil
			for _, sb := range s.superblocks {
				if s.latest == nil || sb.Number > s.latest.Number {
					s.latest = sb
				}
			}
		}
	}

	return nil
}
