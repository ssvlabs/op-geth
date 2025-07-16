package network

import (
	"net"
	"sync"
	"time"
)

// conn wraps a network connection with metadata
type conn struct {
	net.Conn
	id   string
	info ConnectionInfo
	mu   sync.RWMutex
}

// NewConnection creates a new connection wrapper
func NewConnection(netConn net.Conn, id string) Connection {
	return &conn{
		Conn: netConn,
		id:   id,
		info: ConnectionInfo{
			ID:          id,
			RemoteAddr:  netConn.RemoteAddr().String(),
			ConnectedAt: time.Now(),
			LastSeen:    time.Now(),
		},
	}
}

func (c *conn) GetID() string {
	return c.id
}

func (c *conn) GetInfo() ConnectionInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.info
}

func (c *conn) UpdateLastSeen() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.info.LastSeen = time.Now()
}

func (c *conn) SetChainID(chainID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.info.ChainID = chainID
}
