package xt

import (
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	pb "github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/proto/rollup/v1"
)

// hashMapping stores original->final hash mappings for a specific chain within an XT.
type hashMapping struct {
	chainID      string
	originalHash common.Hash
	finalHash    common.Hash
}

// XTResultTracker coordinates one-shot waiters for XT processing results.
type XTResultTracker struct {
	mu            sync.Mutex
	waiters       map[string]chan XTResult
	hashMappings  map[string][]hashMapping // key is xtID hex
	pendingPublish map[string]chan struct{} // notify when ready to publish
}

// NewXTResultTracker constructs an empty tracker.
func NewXTResultTracker() *XTResultTracker {
	return &XTResultTracker{
		waiters:        make(map[string]chan XTResult),
		hashMappings:   make(map[string][]hashMapping),
		pendingPublish: make(map[string]chan struct{}),
	}
}

// Subscribe registers a waiter for the supplied XT ID and returns the channel
// used to deliver the eventual result. The caller also receives a cancel
// function to abandon the wait; cancel is safe to call multiple times.
func (t *XTResultTracker) Subscribe(xtID *pb.XtID) (<-chan XTResult, func(), error) {
	key, err := trackerKey(xtID)
	if err != nil {
		return nil, nil, err
	}

	ch := make(chan XTResult, 1)

	t.mu.Lock()
	if _, exists := t.waiters[key]; exists {
		t.mu.Unlock()
		close(ch)
		return nil, nil, fmt.Errorf("xt tracker subscription already exists for %s", key)
	}
	t.waiters[key] = ch
	t.mu.Unlock()

	cancel := func() {
		waiter := t.pop(key)
		if waiter != nil {
			close(waiter)
		}
	}

	return ch, cancel, nil
}

// UpdateHashMapping records a hash mapping from a remote chain that re-signed a transaction.
// This mapping will be applied when Publish is called.
func (t *XTResultTracker) UpdateHashMapping(xtID *pb.XtID, chainID string, originalHash, finalHash common.Hash) {
	key, err := trackerKey(xtID)
	if err != nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.hashMappings[key] = append(t.hashMappings[key], hashMapping{
		chainID:      chainID,
		originalHash: originalHash,
		finalHash:    finalHash,
	})

	// Notify anyone waiting for hash mappings
	if ch, exists := t.pendingPublish[key]; exists {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// PublishWithWait delivers the successful hashes to any subscriber waiting on xtID.
// It waits for hash mappings from remote chains before publishing (with timeout).
func (t *XTResultTracker) PublishWithWait(xtID *pb.XtID, hashes []ChainTxHash, waitForMappings bool) {
	key, err := trackerKey(xtID)
	if err != nil {
		return
	}

	if waitForMappings {
		// Create notification channel for hash mappings
		notifyCh := make(chan struct{}, 10)
		t.mu.Lock()
		t.pendingPublish[key] = notifyCh
		t.mu.Unlock()

		// Wait for hash mappings with timeout
		timeout := time.After(2 * time.Second)
		lastMappingTime := time.Now()

	waitLoop:
		for {
			select {
			case <-notifyCh:
				// New hash mapping arrived, reset timer
				lastMappingTime = time.Now()
			case <-time.After(200 * time.Millisecond):
				// No new mappings for 200ms, assume we're done
				if time.Since(lastMappingTime) > 200*time.Millisecond {
					break waitLoop
				}
			case <-timeout:
				// Overall timeout
				break waitLoop
			}
		}

		// Clean up
		t.mu.Lock()
		delete(t.pendingPublish, key)
		t.mu.Unlock()
		close(notifyCh)
	}

	// Apply hash mappings before publishing
	t.mu.Lock()
	mappings := t.hashMappings[key]
	delete(t.hashMappings, key) // Clean up mappings after use
	t.mu.Unlock()

	cloned := append([]ChainTxHash(nil), hashes...)

	// Replace any hashes that were re-signed by remote chains
	// We want to return the original hashes that the client knows about
	for i := range cloned {
		for _, mapping := range mappings {
			if cloned[i].ChainID == mapping.chainID && cloned[i].Hash == mapping.finalHash {
				cloned[i].Hash = mapping.originalHash
			}
		}
	}

	waiter := t.pop(key)
	if waiter == nil {
		return
	}

	waiter <- XTResult{Hashes: cloned}
	close(waiter)
}

// Publish delivers the successful hashes to any subscriber waiting on xtID (immediate, no wait).
func (t *XTResultTracker) Publish(xtID *pb.XtID, hashes []ChainTxHash) {
	t.PublishWithWait(xtID, hashes, false)
}

// PublishError notifies the subscriber that processing failed.
func (t *XTResultTracker) PublishError(xtID *pb.XtID, err error) {
	key, e := trackerKey(xtID)
	if e != nil {
		return
	}

	waiter := t.pop(key)
	if waiter == nil {
		return
	}

	waiter <- XTResult{Err: err}
	close(waiter)
}

func (t *XTResultTracker) pop(key string) chan XTResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.waiters == nil {
		return nil
	}
	waiter, ok := t.waiters[key]
	if ok {
		delete(t.waiters, key)
	}
	return waiter
}

func trackerKey(xtID *pb.XtID) (string, error) {
	if xtID == nil || len(xtID.Hash) == 0 {
		return "", fmt.Errorf("invalid xtID")
	}
	return xtID.Hex(), nil
}
