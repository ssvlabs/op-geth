package network

import (
	"context"
	"net"
	"time"

	xt "github.com/ethereum/go-ethereum/internal/xt"
)

// Server interface defines the server contract
type Server interface {
	// Start starts the server
	Start(ctx context.Context) error
	// Stop gracefully stops the server
	Stop(ctx context.Context) error
	// SendToSP sends a message to a shared publisher
	SendToSP(ctx context.Context, msg *xt.Message) error
	// SetHandler sets the message handler
	SetHandler(handler MessageHandler)
}

// Client interface defines the client contract
type Client interface {
	// Connect establishes connection to the server
	Connect(ctx context.Context) error
	// Disconnect closes the connection
	Disconnect(ctx context.Context) error
	// Send sends a message to the server
	Send(ctx context.Context, msg *xt.Message) error
	// SetHandler sets the message handler for received messages
	SetHandler(handler MessageHandler)
	// IsConnected returns connection status
	IsConnected() bool
	// GetID returns the client identifier
	GetID() string
}

// MessageHandler processes incoming messages
type MessageHandler func(ctx context.Context, from string, msg *xt.Message) error

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
