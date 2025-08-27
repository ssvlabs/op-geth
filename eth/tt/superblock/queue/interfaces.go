package queue

import (
	"context"
)

type XTRequestQueue interface {
	Enqueue(ctx context.Context, request *QueuedXTRequest) error
	Dequeue(ctx context.Context) (*QueuedXTRequest, error)
	Peek(ctx context.Context) (*QueuedXTRequest, error)
	Size(ctx context.Context) (int, error)
	RemoveExpired(ctx context.Context) (int, error)
	RequeueForSlot(ctx context.Context, requests []*QueuedXTRequest) error
}
