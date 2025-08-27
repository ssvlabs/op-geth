// Package queue. This memory store is for testing purposes only. True production store will be implemented later.
// So im adding a TODO comment here.
package queue

import (
	"container/heap"
	"context"
	"fmt"
	"sync"
	"time"
)

// PriorityQueue implements a priority queue for QueuedXTRequest
type PriorityQueue []*QueuedXTRequest

func (pq PriorityQueue) Len() int { return len(pq) }

func (pq PriorityQueue) Less(i, j int) bool {
	// Higher priority first (larger number = higher priority)
	return pq[i].Priority > pq[j].Priority
}

func (pq PriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
}

func (pq *PriorityQueue) Push(x interface{}) {
	*pq = append(*pq, x.(*QueuedXTRequest))
}

func (pq *PriorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	*pq = old[0 : n-1]
	return item
}

// MemoryXTRequestQueue provides in-memory priority queue for cross-chain transaction requests
type MemoryXTRequestQueue struct {
	mu       sync.RWMutex
	queue    PriorityQueue
	maxSize  int
	heapInit bool
}

func NewMemoryXTRequestQueue(config Config) *MemoryXTRequestQueue {
	return &MemoryXTRequestQueue{
		queue:   make(PriorityQueue, 0),
		maxSize: config.MaxSize,
	}
}

func (q *MemoryXTRequestQueue) ensureHeap() {
	if !q.heapInit && len(q.queue) > 0 {
		heap.Init(&q.queue)
		q.heapInit = true
	}
}

func (q *MemoryXTRequestQueue) Enqueue(_ context.Context, request *QueuedXTRequest) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.queue) >= q.maxSize {
		return fmt.Errorf("queue full: %d/%d", len(q.queue), q.maxSize)
	}

	q.ensureHeap()
	heap.Push(&q.queue, request)

	return nil
}

func (q *MemoryXTRequestQueue) Dequeue(context.Context) (*QueuedXTRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.queue) == 0 {
		return nil, fmt.Errorf("queue empty")
	}

	q.ensureHeap()
	request := heap.Pop(&q.queue).(*QueuedXTRequest)

	return request, nil
}

func (q *MemoryXTRequestQueue) Peek(context.Context) (*QueuedXTRequest, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	if len(q.queue) == 0 {
		return nil, nil // Return nil without error for empty queue (coordinator handles this)
	}

	q.ensureHeap()
	return q.queue[0], nil
}

func (q *MemoryXTRequestQueue) Size(context.Context) (int, error) {
	q.mu.RLock()
	defer q.mu.RUnlock()

	return len(q.queue), nil
}

func (q *MemoryXTRequestQueue) RemoveExpired(context.Context) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	now := time.Now()
	removed := 0

	var newQueue PriorityQueue
	for _, request := range q.queue {
		if request.ExpiresAt.After(now) {
			newQueue = append(newQueue, request)
		} else {
			removed++
		}
	}

	q.queue = newQueue
	q.heapInit = false

	return removed, nil
}

func (q *MemoryXTRequestQueue) RequeueForSlot(_ context.Context, requests []*QueuedXTRequest) error {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, request := range requests {
		if len(q.queue) >= q.maxSize {
			return fmt.Errorf("queue full during requeue: %d/%d", len(q.queue), q.maxSize)
		}

		request.Attempts++
		request.Priority -= int64(request.Attempts * 10) // Lower priority for retries

		q.queue = append(q.queue, request)
	}

	q.heapInit = false
	return nil
}
