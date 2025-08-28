package transport

import (
	"context"
	"net"
	"time"

	"github.com/ethereum/go-ethereum/common"
	pb "github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/proto/rollup/v1"
	"github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/x/auth"
)

// Transport defines the network transport interface
type Transport interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Broadcast(ctx context.Context, msg *pb.Message, excludeID string) error
	Send(ctx context.Context, clientID string, msg *pb.Message) error
	SetHandler(handler ServerMessageHandler)
	GetConnections() []ConnectionInfo
}

// Server interface for accepting connections
type Server interface {
	Transport
	Listen(ctx context.Context, addr string) error
}

// ServerMessageHandler processes incoming messages
type ServerMessageHandler func(ctx context.Context, from string, msg *pb.Message) error

// Client interface for outbound connections
type Client interface {
	Connect(ctx context.Context, addr string) error
	Disconnect(ctx context.Context) error
	Send(ctx context.Context, msg *pb.Message) error
	IsConnected() bool
	GetID() string

	Reconnect(ctx context.Context) error
	ConnectWithRetry(ctx context.Context, addr string, maxRetries int) error
	GetStats() map[string]interface{}
	SetHandler(handler ClientMessageHandler)
	SetReadTimeout(timeout time.Duration)
	SetWriteTimeout(timeout time.Duration)
	SetReconnectDelay(delay time.Duration)

	WithAuth(authManager auth.Manager) Client
}

// ClientMessageHandler processes incoming messages
type ClientMessageHandler func(ctx context.Context, msg *pb.Message) ([]common.Hash, error)

// Connection represents a network connection with metadata and authentication
type Connection interface {
	net.Conn
	ID() string
	Info() ConnectionInfo
	UpdateLastSeen()
	SetChainID(chainID string)
	WriteMessage(msg *pb.Message) error
	ReadMessage() (*pb.Message, error)

	IsAuthenticated() bool
	GetAuthenticatedID() string // empty if not authenticated
	PerformHandshake(signer auth.Manager, clientID string) error
	HandleHandshake(authManager auth.Manager) error
}

// ConnectionInfo contains connection metadata
type ConnectionInfo struct {
	ID           string
	RemoteAddr   string
	ConnectedAt  time.Time
	LastSeen     time.Time
	ChainID      string
	BytesRead    uint64
	BytesWritten uint64
}

// Config holds transport configuration
type Config struct {
	ListenAddr      string
	MaxConnections  int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	MaxMessageSize  int
	BufferSize      int
	KeepAlive       bool
	KeepAlivePeriod time.Duration
}

// DefaultConfig returns sensible defaults
func DefaultConfig() Config {
	return Config{
		ListenAddr:      ":8080",
		MaxConnections:  1000,
		ReadTimeout:     30 * time.Second,
		WriteTimeout:    30 * time.Second,
		MaxMessageSize:  10 * 1024 * 1024, // 10MB
		BufferSize:      8192,
		KeepAlive:       true,
		KeepAlivePeriod: 30 * time.Second,
	}
}
