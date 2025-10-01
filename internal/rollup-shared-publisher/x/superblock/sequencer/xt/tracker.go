package xt

import (
	"fmt"
	"sync"

	pb "github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/proto/rollup/v1"
)

// XTResultTracker coordinates one-shot waiters for XT processing results.
type XTResultTracker struct {
	mu      sync.Mutex
	waiters map[string]chan XTResult
}

// NewXTResultTracker constructs an empty tracker.
func NewXTResultTracker() *XTResultTracker {
	return &XTResultTracker{
		waiters: make(map[string]chan XTResult),
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

// Publish delivers the successful hashes to any subscriber waiting on xtID.
func (t *XTResultTracker) Publish(xtID *pb.XtID, hashes []ChainTxHash) {
	key, err := trackerKey(xtID)
	if err != nil {
		return
	}

	waiter := t.pop(key)
	if waiter == nil {
		return
	}

	cloned := append([]ChainTxHash(nil), hashes...)
	waiter <- XTResult{Hashes: cloned}
	close(waiter)
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
