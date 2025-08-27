package registry

import (
	"context"
	"time"
)

type Service interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	GetActiveRollups(ctx context.Context) ([][]byte, error)
	GetRollupEndpoint(ctx context.Context, chainID []byte) (string, error)
	GetRollupPublicKey(ctx context.Context, chainID []byte) ([]byte, error)
	IsRollupActive(ctx context.Context, chainID []byte) (bool, error)
	WatchRegistry(ctx context.Context) (<-chan Event, error)
	GetRollupInfo(chainID []byte) (*RollupInfo, error)
	GetAllRollups() map[string]*RollupInfo
	SetPollingInterval(interval time.Duration)
}
