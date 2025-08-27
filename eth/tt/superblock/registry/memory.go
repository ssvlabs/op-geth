package registry

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// memoryService is a simple in-memory registry for dev/testing.
// It satisfies Service and returns a static set of active rollups.
type memoryService struct {
	mu       sync.RWMutex
	chainIDs [][]byte
	log      zerolog.Logger

	eventCh chan Event
}

func NewMemoryService(log zerolog.Logger, chainIDs [][]byte) Service {
	ids := make([][]byte, len(chainIDs))
	for i := range chainIDs {
		if chainIDs[i] == nil {
			continue
		}
		b := make([]byte, len(chainIDs[i]))
		copy(b, chainIDs[i])
		ids[i] = b
	}
	return &memoryService{
		chainIDs: ids,
		log:      log.With().Str("component", "registry.memory").Logger(),
		eventCh:  make(chan Event, 1),
	}
}

func (m *memoryService) Start(context.Context) error { return nil }
func (m *memoryService) Stop(context.Context) error  { return nil }

func (m *memoryService) GetActiveRollups(context.Context) ([][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([][]byte, len(m.chainIDs))
	for i := range m.chainIDs {
		b := make([]byte, len(m.chainIDs[i]))
		copy(b, m.chainIDs[i])
		out[i] = b
	}
	return out, nil
}

func (m *memoryService) GetRollupEndpoint(context.Context, []byte) (string, error)  { return "", nil }
func (m *memoryService) GetRollupPublicKey(context.Context, []byte) ([]byte, error) { return nil, nil }
func (m *memoryService) IsRollupActive(context.Context, []byte) (bool, error)       { return true, nil }

func (m *memoryService) WatchRegistry(context.Context) (<-chan Event, error) {
	// Never emits in-memory changes; return channel for API compatibility.
	return m.eventCh, nil
}

func (m *memoryService) GetRollupInfo([]byte) (*RollupInfo, error) { return nil, nil }
func (m *memoryService) GetAllRollups() map[string]*RollupInfo     { return map[string]*RollupInfo{} }
func (m *memoryService) SetPollingInterval(time.Duration)          {}
