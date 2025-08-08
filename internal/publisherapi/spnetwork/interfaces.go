package network

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
	rollupv1 "github.com/ssvlabs/rollup-shared-publisher/proto/rollup/v1"

	"net"
	"time"
)

// Server interface defines the server contract
type Server interface {
	// Start starts the server
	Start(ctx context.Context) error
	// Stop gracefully stops the server
	Stop(ctx context.Context) error
	// SetHandler sets the message handler
	SetHandler(handler MessageHandler)
}

// Client interface defines the client contract
type Client interface {
	// Connect establishes connection to the server
	Connect(ctx context.Context, mandatory bool) error
	// Disconnect closes the connection
	Disconnect(ctx context.Context) error
	// Send sends a message to the server
	Send(ctx context.Context, msg *rollupv1.Message) error
	// SetHandler sets the message handler for received messages
	SetHandler(handler MessageHandler)
	// IsConnected returns connection status
	IsConnected() bool
	// GetID returns the client identifier
	GetID() string
	// Reconnect reestablishes connection to the server
	Reconnect(ctx context.Context) error
}

// MessageHandler processes incoming messages
type MessageHandler func(ctx context.Context, msg *rollupv1.Message) ([]common.Hash, error)

// ConnectionInfo contains information about a connection
type ConnectionInfo struct {
	ID          string
	RemoteAddr  string
	ConnectedAt time.Time
	LastSeen    time.Time
	ChainID     string
}

// Connection represents a network connection
type Connection interface {
	net.Conn
	GetID() string
	GetInfo() ConnectionInfo
	UpdateLastSeen()
}
