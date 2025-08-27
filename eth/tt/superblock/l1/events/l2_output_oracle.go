package events

import (
	"context"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// WatchOutputProposed subscribes to OutputProposed events and emits normalized events.
func WatchOutputProposed(
	ctx context.Context,
	client LogClient,
	contractAddr common.Address,
	contractABI abi.ABI,
) (<-chan *SuperblockEvent, error) {
	logsCh := make(chan types.Log, 128)
	q := ethereum.FilterQuery{
		Addresses: []common.Address{contractAddr},
		Topics:    [][]common.Hash{{contractABI.Events["OutputProposed"].ID}},
	}

	sub, err := client.SubscribeFilterLogs(ctx, q, logsCh)
	if err != nil {
		return nil, err
	}

	out := make(chan *SuperblockEvent, 128)

	go func() {
		defer close(out)
		defer sub.Unsubscribe()

		for {
			select {
			case <-ctx.Done():
				return
			case err := <-sub.Err():
				_ = err // Optional: log via caller
				return
			case lg := <-logsCh:
				// Decode event
				var payload struct {
					Block superBlockTuple `abi:"_block"`
				}
				if err := contractABI.UnpackIntoInterface(&payload, "OutputProposed", lg.Data); err != nil {
					// Skip undecodable logs
					continue
				}

				// Compute superblock header hash (must match coordinator logic)
				hash := computeSuperblockHash(
					payload.Block.BlockNumber,
					payload.Block.Slot,
					payload.Block.ParentBlockHash,
					payload.Block.MerkleRoot,
				)

				// Get L1 timestamp from header
				ts := time.Now()
				if hdr, err := client.HeaderByNumber(ctx, new(big.Int).SetUint64(lg.BlockNumber)); err == nil &&
					hdr != nil {
					ts = time.Unix(int64(hdr.Time), 0)
				}

				evType := SuperblockEventSubmitted
				if lg.Removed {
					evType = SuperblockEventRolledBack
				}
				out <- &SuperblockEvent{
					Type:              evType,
					SuperblockNumber:  payload.Block.BlockNumber,
					SuperblockHash:    hash,
					L1BlockNumber:     lg.BlockNumber,
					L1TransactionHash: lg.TxHash.Bytes(),
					Removed:           lg.Removed,
					Timestamp:         ts,
				}
			}
		}
	}()

	return out, nil
}

func computeSuperblockHash(number uint64, slot uint64, parentHash, merkleRoot []byte) []byte {
	nb := make([]byte, 8)
	sb := make([]byte, 8)
	// Big-endian for consistency with coordinator
	nb[0] = byte(number >> 56)
	nb[1] = byte(number >> 48)
	nb[2] = byte(number >> 40)
	nb[3] = byte(number >> 32)
	nb[4] = byte(number >> 24)
	nb[5] = byte(number >> 16)
	nb[6] = byte(number >> 8)
	nb[7] = byte(number)

	sb[0] = byte(slot >> 56)
	sb[1] = byte(slot >> 48)
	sb[2] = byte(slot >> 40)
	sb[3] = byte(slot >> 32)
	sb[4] = byte(slot >> 24)
	sb[5] = byte(slot >> 16)
	sb[6] = byte(slot >> 8)
	sb[7] = byte(slot)

	header := append(append(append([]byte{}, nb...), sb...), parentHash...)
	header = append(header, merkleRoot...)
	return crypto.Keccak256(header)
}
