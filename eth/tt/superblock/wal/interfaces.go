package wal

import (
	"context"
)

type Manager interface {
	WriteEntry(ctx context.Context, entry *Entry) error
	ReadEntries(ctx context.Context, checkpoint uint64) ([]*Entry, error)
	Checkpoint(ctx context.Context, slot uint64) error
	Truncate(ctx context.Context, beforeSlot uint64) error
	Close() error
}
