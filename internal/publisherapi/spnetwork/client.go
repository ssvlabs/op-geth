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
	"github.com/rs/zerolog"
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
	log     zerolog.Logger

	conn      net.Conn
	writer    *StreamWriter
	connected atomic.Bool
	mu        sync.RWMutex

	// Shutdown management
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewClient creates a new client instance.
func NewClient(cfg ClientConfig, log zerolog.Logger) Client {
	return &client{
		cfg:   cfg,
		id:    uuid.New().String(),
		codec: NewCodec(cfg.MaxMessageSize),
		log:   log.With().Str("component", "client").Logger(),
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
		return fmt.Errorf("failed to connect: %w", err)
	}

	c.conn = conn
	c.writer = NewStreamWriter(conn, c.codec)
	c.connected.Store(true)

	ctx, c.cancel = context.WithCancel(context.Background())

	c.wg.Add(1)
	go c.receiveLoop(ctx)

	c.log.Info().
		Str("server", c.cfg.ServerAddr).
		Str("client_id", c.id).
		Msg("Connected to server")

	return nil
}

// Disconnect closes the connection.
func (c *client) Disconnect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected.Load() {
		return ErrNotConnected
	}

	c.log.Info().Msg("Disconnecting")

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
		c.log.Info().Msg("Disconnected")
	case <-ctx.Done():
		c.log.Warn().Msg("Disconnect timeout")
		return ctx.Err()
	}

	c.connected.Store(false)
	return nil
}

// Send sends a message to the server.
func (c *client) Send(_ context.Context, msg *xt.Message) error {
	c.mu.RLock()
	writer := c.writer
	c.mu.RUnlock()

	if !c.connected.Load() || writer == nil {
		return ErrNotConnected
	}

	msg.SenderId = c.id

	return writer.Write(msg)
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
		c.log.Info().Msg("Receive loop ended")
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
					c.log.Debug().Msg("Server closed connection")
				} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue // Read timeout, continue
				} else {
					c.log.Error().Err(err).Msg("Read error")
				}
				return
			}

			if c.handler != nil {
				if err := c.handler(ctx, msg.SenderId, &msg); err != nil {
					c.log.Error().Err(err).Msg("Handler error")
				}
			}
		}
	}
}

// Reconnect attempts to reconnect to the server.
func (c *client) Reconnect(ctx context.Context) error {
	if err := c.Disconnect(ctx); err != nil {
		c.log.Warn().Err(err).Msg("Error during disconnect before reconnect")
	}

	time.Sleep(c.cfg.ReconnectDelay)
	return c.Connect(ctx)
}
