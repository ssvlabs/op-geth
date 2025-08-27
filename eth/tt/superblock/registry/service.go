package registry

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ssvlabs/rollup-shared-publisher/log"
)

type RollupInfo struct {
	ChainID      []byte
	Endpoint     string
	PublicKey    []byte
	StartingSlot uint64
	IsActive     bool
	UpdatedAt    time.Time
}

type Event struct {
	Type      EventType
	ChainID   []byte
	Endpoint  string
	PublicKey []byte
	Timestamp time.Time
}

type EventType string

const (
	EventTypeRollupAdded   EventType = "rollup_added"
	EventTypeRollupRemoved EventType = "rollup_removed"
	EventTypeRollupUpdated EventType = "rollup_updated"
)

type service struct {
	mu           sync.RWMutex
	rollups      map[string]*RollupInfo
	logger       *log.Logger
	l1Client     L1Client
	contractAddr string

	eventCh   chan Event
	stopCh    chan struct{}
	watcherWg sync.WaitGroup

	pollingInterval time.Duration
	lastSyncedBlock uint64
}

type L1Client interface {
	GetLatestBlockNumber(ctx context.Context) (uint64, error)
	GetBlockByNumber(ctx context.Context, blockNumber uint64) (*L1Block, error)
	GetContractEvents(ctx context.Context, contractAddr string, fromBlock, toBlock uint64) ([]ContractEvent, error)
	SubscribeToContractEvents(ctx context.Context, contractAddr string) (<-chan ContractEvent, error)
}

type L1Block struct {
	Number    uint64
	Hash      []byte
	Timestamp time.Time
}

type ContractEvent struct {
	Type        string
	ChainID     []byte
	Endpoint    string
	PublicKey   []byte
	BlockNumber uint64
	TxHash      []byte
}

func NewService(logger *log.Logger, l1Client L1Client, contractAddr string) Service {
	return &service{
		rollups:         make(map[string]*RollupInfo),
		logger:          logger,
		l1Client:        l1Client,
		contractAddr:    contractAddr,
		eventCh:         make(chan Event, 100),
		stopCh:          make(chan struct{}),
		pollingInterval: 30 * time.Second,
	}
}

func (s *service) Start(ctx context.Context) error {
	s.logger.Info().Str("contract_addr", s.contractAddr).Msg("Starting registry service")

	if err := s.syncFromL1(ctx); err != nil {
		return fmt.Errorf("initial sync failed: %w", err)
	}

	s.watcherWg.Add(1)
	go s.watchEvents(ctx)

	return nil
}

func (s *service) Stop(ctx context.Context) error {
	s.logger.Info().Msg("Stopping registry service")

	close(s.stopCh)
	s.watcherWg.Wait()
	close(s.eventCh)

	return nil
}

func (s *service) GetActiveRollups(ctx context.Context) ([][]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var activeChainIDs [][]byte
	for _, rollup := range s.rollups {
		if rollup.IsActive {
			activeChainIDs = append(activeChainIDs, rollup.ChainID)
		}
	}

	return activeChainIDs, nil
}

func (s *service) GetRollupEndpoint(ctx context.Context, chainID []byte) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rollup, exists := s.rollups[string(chainID)]
	if !exists || !rollup.IsActive {
		return "", fmt.Errorf("rollup not found or inactive: %x", chainID)
	}

	return rollup.Endpoint, nil
}

func (s *service) GetRollupPublicKey(ctx context.Context, chainID []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rollup, exists := s.rollups[string(chainID)]
	if !exists || !rollup.IsActive {
		return nil, fmt.Errorf("rollup not found or inactive: %x", chainID)
	}

	return rollup.PublicKey, nil
}

func (s *service) IsRollupActive(_ context.Context, chainID []byte) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rollup, exists := s.rollups[string(chainID)]
	return exists && rollup.IsActive, nil
}

func (s *service) WatchRegistry(ctx context.Context) (<-chan Event, error) {
	return s.eventCh, nil
}

func (s *service) GetRollupInfo(chainID []byte) (*RollupInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rollup, exists := s.rollups[string(chainID)]
	if !exists {
		return nil, fmt.Errorf("rollup not found: %x", chainID)
	}

	return &RollupInfo{
		ChainID:      make([]byte, len(rollup.ChainID)),
		Endpoint:     rollup.Endpoint,
		PublicKey:    make([]byte, len(rollup.PublicKey)),
		StartingSlot: rollup.StartingSlot,
		IsActive:     rollup.IsActive,
		UpdatedAt:    rollup.UpdatedAt,
	}, nil
}

func (s *service) GetAllRollups() map[string]*RollupInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]*RollupInfo)
	for k, v := range s.rollups {
		result[k] = &RollupInfo{
			ChainID:      make([]byte, len(v.ChainID)),
			Endpoint:     v.Endpoint,
			PublicKey:    make([]byte, len(v.PublicKey)),
			StartingSlot: v.StartingSlot,
			IsActive:     v.IsActive,
			UpdatedAt:    v.UpdatedAt,
		}
		copy(result[k].ChainID, v.ChainID)
		copy(result[k].PublicKey, v.PublicKey)
	}
	return result
}

func (s *service) syncFromL1(ctx context.Context) error {
	latestBlock, err := s.l1Client.GetLatestBlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("failed to get latest block: %w", err)
	}

	fromBlock := s.lastSyncedBlock
	if fromBlock == 0 {
		fromBlock = latestBlock - 10000
		if latestBlock < 10000 {
			fromBlock = 0
		}
	}

	events, err := s.l1Client.GetContractEvents(ctx, s.contractAddr, fromBlock, latestBlock)
	if err != nil {
		return fmt.Errorf("failed to get contract events: %w", err)
	}

	s.logger.Info().
		Uint64("from_block", fromBlock).
		Uint64("to_block", latestBlock).
		Int("events_count", len(events)).
		Msg("Syncing registry events")

	for _, event := range events {
		s.processContractEvent(event)
	}

	s.lastSyncedBlock = latestBlock
	return nil
}

func (s *service) watchEvents(ctx context.Context) {
	defer s.watcherWg.Done()

	ticker := time.NewTicker(s.pollingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			if err := s.syncFromL1(ctx); err != nil {
				s.logger.Error().Err(err).Msg("Failed to sync from L1")
			}
		}
	}
}

func (s *service) processContractEvent(contractEvent ContractEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	chainIDStr := string(contractEvent.ChainID)
	now := time.Now()

	var eventType EventType

	switch contractEvent.Type {
	case "RollupRegistered":
		s.rollups[chainIDStr] = &RollupInfo{
			ChainID:      contractEvent.ChainID,
			Endpoint:     contractEvent.Endpoint,
			PublicKey:    contractEvent.PublicKey,
			StartingSlot: 1,
			IsActive:     true,
			UpdatedAt:    now,
		}
		eventType = EventTypeRollupAdded

		s.logger.Info().
			Str("chain_id", fmt.Sprintf("%x", contractEvent.ChainID)).
			Str("endpoint", contractEvent.Endpoint).
			Msg("Rollup registered")

	case "RollupUpdated":
		if rollup, exists := s.rollups[chainIDStr]; exists {
			rollup.Endpoint = contractEvent.Endpoint
			rollup.PublicKey = contractEvent.PublicKey
			rollup.UpdatedAt = now
			eventType = EventTypeRollupUpdated

			s.logger.Info().
				Str("chain_id", fmt.Sprintf("%x", contractEvent.ChainID)).
				Str("endpoint", contractEvent.Endpoint).
				Msg("Rollup updated")
		}

	case "RollupDeactivated":
		if rollup, exists := s.rollups[chainIDStr]; exists {
			rollup.IsActive = false
			rollup.UpdatedAt = now
			eventType = EventTypeRollupRemoved

			s.logger.Info().Str("chain_id", fmt.Sprintf("%x", contractEvent.ChainID)).Msg("Rollup deactivated")
		}
	}

	select {
	case s.eventCh <- Event{
		Type:      eventType,
		ChainID:   contractEvent.ChainID,
		Endpoint:  contractEvent.Endpoint,
		PublicKey: contractEvent.PublicKey,
		Timestamp: now,
	}:
	default:
		s.logger.Warn().Msg("Registry event channel full, dropping event")
	}
}

func (s *service) SetPollingInterval(interval time.Duration) {
	s.pollingInterval = interval
}
