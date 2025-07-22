package network

import (
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum/internal/xt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/ethereum/go-ethereum/log"
)

// ClientConfig contains client configuration.
type ClientConfig struct {
	ServerAddr     string
	ConnectTimeout time.Duration
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	ReconnectDelay time.Duration
	MaxMessageSize int
}

// client implements the Client interface.
type client struct {
	cfg     ClientConfig
	id      string
	handler MessageHandler
	codec   *Codec

	conn      net.Conn
	writer    *StreamWriter
	connected atomic.Bool
	mu        sync.RWMutex

	// Shutdown management
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewClient creates a new client instance.
func NewClient(cfg ClientConfig) Client {
	return &client{
		cfg:   cfg,
		id:    uuid.New().String(),
		codec: NewCodec(cfg.MaxMessageSize),
	}
}

// Connect establishes connection to the server.
func (c *client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.connected.Load() {
		return ErrAlreadyConnected
	}

	connCtx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout)
	defer cancel()

	dialer := &net.Dialer{
		Timeout:   c.cfg.ConnectTimeout,
		KeepAlive: 30 * time.Second,
	}

	conn, err := dialer.DialContext(connCtx, "tcp", c.cfg.ServerAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to SP server: %w", err)
	}

	c.conn = conn
	c.writer = NewStreamWriter(conn, c.codec)
	c.connected.Store(true)

	ctx, c.cancel = context.WithCancel(context.Background())

	c.wg.Add(1)
	go c.receiveLoop(ctx)

	log.Info("Connected to SP server", "server", c.cfg.ServerAddr)

	return nil
}

// Disconnect closes the connection.
func (c *client) Disconnect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected.Load() {
		return ErrNotConnected
	}

	log.Info("Disconnecting")

	if c.cancel != nil {
		c.cancel()
	}

	if c.conn != nil {
		c.conn.Close()
	}

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Info("Disconnected")
	case <-ctx.Done():
		log.Warn("Disconnect timeout")
		return ctx.Err()
	}

	c.connected.Store(false)
	return nil
}

func (c *client) Send(ctx context.Context, msg *xt.Message) error {
	const maxRetries = 3
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		c.mu.RLock()
		writer := c.writer
		connected := c.connected.Load()
		c.mu.RUnlock()

		if !connected || writer == nil {
			// Attempt to reconnect
			if err := c.Reconnect(ctx); err != nil {
				lastErr = err
				time.Sleep(c.cfg.ReconnectDelay)
				continue
			}

			c.mu.RLock()
			writer = c.writer
			c.mu.RUnlock()
		}

		msg.SenderId = c.id
		if err := writer.Write(msg); err != nil {
			lastErr = err
			c.connected.Store(false)
			time.Sleep(c.cfg.ReconnectDelay)
			continue
		}

		return nil
	}

	return fmt.Errorf("failed to send after %d reconnect retries: %w", maxRetries, lastErr)
}

// SetHandler sets the message handler.
func (c *client) SetHandler(handler MessageHandler) {
	c.handler = handler
}

// IsConnected returns connection status.
func (c *client) IsConnected() bool {
	return c.connected.Load()
}

// GetID returns the client ID.
func (c *client) GetID() string {
	return c.id
}

// receiveLoop reads messages from the server.
func (c *client) receiveLoop(ctx context.Context) {
	defer c.wg.Done()
	defer func() {
		c.connected.Store(false)
		log.Info("Connection to SP closed")
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
			if c.cfg.ReadTimeout > 0 {
				_ = c.conn.SetReadDeadline(time.Now().Add(c.cfg.ReadTimeout))
			}

			var msg xt.Message
			if err := c.codec.Decode(c.conn, &msg); err != nil {
				if err == io.EOF {
					log.Debug("Server closed connection")
				} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue // Read timeout, continue
				} else {
					log.Error("Read error", "err", err)
				}
				return
			}

			if c.handler != nil {
				if _, err := c.handler(ctx, msg.SenderId, &msg); err != nil {
					log.Error("Handler error", "err", err)
				}
			}
		}
	}
}

// Reconnect attempts to reconnect to the server.
func (c *client) Reconnect(ctx context.Context) error {
	if c.IsConnected() {
		if err := c.Disconnect(ctx); err != nil {
			log.Warn("Error during disconnect before reconnect", "err", err)
		}
	}

	return c.Connect(ctx)
}
