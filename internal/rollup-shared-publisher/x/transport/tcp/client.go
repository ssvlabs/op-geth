package tcp

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/x/auth"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	pb "github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/proto/rollup/v1"
	"github.com/ethereum/go-ethereum/internal/rollup-shared-publisher/x/transport"
)

// ClientConfig contains client-specific configuration
type ClientConfig struct {
	ServerAddr      string
	ConnectTimeout  time.Duration
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ReconnectDelay  time.Duration
	MaxMessageSize  int
	KeepAlive       bool
	KeepAlivePeriod time.Duration

	// Optional custom client identifier. If empty, a new uuid.NewV7() will be generated (fallback to uuid.New())
	ClientID string
}

// DefaultClientConfig returns sensible client defaults
func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		ServerAddr:      "localhost:8080",
		ConnectTimeout:  10 * time.Second,
		ReadTimeout:     30 * time.Second,
		WriteTimeout:    10 * time.Second,
		ReconnectDelay:  5 * time.Second,
		MaxMessageSize:  10 * 1024 * 1024, // 10MB
		KeepAlive:       true,
		KeepAlivePeriod: 30 * time.Second,
	}
}

// client implements the transport.Client interface
type client struct {
	config      ClientConfig
	id          string
	handler     transport.ClientMessageHandler
	codec       *Codec
	authManager auth.Manager // Optional authentication
	log         zerolog.Logger

	// Connection management
	conn          transport.Connection
	connected     atomic.Bool
	mu            sync.RWMutex
	reconnecting  atomic.Bool
	autoReconnect atomic.Bool

	// Shutdown management
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Metrics
	metrics *transport.Metrics
}

// NewClient creates a new TCP client instance
func NewClient(config ClientConfig, log zerolog.Logger) transport.Client {
	id := config.ClientID
	if id == "" {
		if u, err := uuid.NewV7(); err == nil {
			id = u.String()
		} else {
			id = uuid.New().String()
		}
	}

	c := &client{
		config:  config,
		id:      id,
		codec:   NewCodec(config.MaxMessageSize),
		log:     log.With().Str("component", "tcp-client").Logger(),
		metrics: transport.NewMetrics(id),
	}

	c.autoReconnect.Store(true)

	return c
}

// WithAuth adds authentication to the client
func (c *client) WithAuth(authManager auth.Manager) transport.Client {
	c.authManager = authManager
	return c
}

// Connect establishes connection to the server
func (c *client) Connect(ctx context.Context, addr string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.connected.Load() {
		return fmt.Errorf("client already connected")
	}

	if addr != "" {
		c.config.ServerAddr = addr
	}

	receiveCtx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	connCtx, cancel := context.WithTimeout(ctx, c.config.ConnectTimeout)
	defer cancel()

	dialer := &net.Dialer{
		Timeout:   c.config.ConnectTimeout,
		KeepAlive: c.config.KeepAlivePeriod,
	}

	netConn, err := dialer.DialContext(connCtx, "tcp", c.config.ServerAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", c.config.ServerAddr, err)
	}

	// Configure TCP options
	if tcpConn, ok := netConn.(*net.TCPConn); ok {
		tcpConn.SetKeepAlive(c.config.KeepAlive)
		if c.config.KeepAlive {
			tcpConn.SetKeepAlivePeriod(c.config.KeepAlivePeriod)
		}
		tcpConn.SetNoDelay(true) // Low latency
	}

	// Create connection wrapper with same codec (includes auth if configured)
	c.conn = NewConnection(netConn, c.id, c.codec, c.log)

	if c.authManager != nil {
		if err := c.conn.PerformHandshake(c.authManager, c.id); err != nil {
			netConn.Close()
			return fmt.Errorf("handshake failed: %w", err)
		}
	}

	c.connected.Store(true)

	// Start receive loop
	c.wg.Add(1)
	go c.receiveLoop(receiveCtx)

	// Start connection monitor for auto-reconnect
	c.wg.Add(1)
	go c.connectionMonitor(receiveCtx)

	c.metrics.RecordConnection("connected")

	logEntry := c.log.Info().
		Str("server", c.config.ServerAddr).
		Str("client_id", c.id)

	if c.authManager != nil {
		logEntry = logEntry.
			Str("address", c.authManager.Address()).
			Str("auth", "enabled")
	} else {
		logEntry = logEntry.Str("auth", "disabled")
	}

	logEntry.Msg("Connected to server")

	return nil
}

// Disconnect closes the connection
func (c *client) Disconnect(ctx context.Context) error {
	c.autoReconnect.Store(false)
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected.Load() {
		return fmt.Errorf("client not connected")
	}

	c.log.Info().Msg("Disconnecting from server")

	// Signal shutdown
	if c.cancel != nil {
		c.cancel()
	}

	// Close connection
	if c.conn != nil {
		c.conn.Close()
	}

	// Wait for goroutines with timeout
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		c.log.Info().Msg("Disconnected from server")
	case <-ctx.Done():
		c.log.Warn().Msg("Disconnect timeout")
		return ctx.Err()
	}

	c.connected.Store(false)
	c.metrics.RecordConnection("disconnected")
	return nil
}

// Send sends a message to the server
func (c *client) Send(ctx context.Context, msg *pb.Message) error {
	c.mu.RLock()
	conn := c.conn
	connected := c.connected.Load()
	c.mu.RUnlock()

	if !connected || conn == nil {
		return fmt.Errorf("client not connected")
	}

	// Set sender ID
	msg.SenderId = c.id

	// Set write timeout
	if c.config.WriteTimeout > 0 {
		deadline := time.Now().Add(c.config.WriteTimeout)
		if err := conn.SetWriteDeadline(deadline); err != nil {
			return fmt.Errorf("failed to set write deadline: %w", err)
		}
	}

	// Send message
	if err := conn.WriteMessage(msg); err != nil {
		c.log.Error().Err(err).Msg("Failed to send message")
		return fmt.Errorf("send failed: %w", err)
	}

	c.log.Debug().
		Str("msg_type", fmt.Sprintf("%T", msg.Payload)).
		Msg("Message sent")

	return nil
}

// SetHandler sets the message handler
func (c *client) SetHandler(handler transport.ClientMessageHandler) {
	c.handler = handler
}

// IsConnected returns connection status
func (c *client) IsConnected() bool {
	return c.connected.Load()
}

// GetID returns the client ID
func (c *client) GetID() string {
	return c.id
}

// receiveLoop reads messages from the server
func (c *client) receiveLoop(ctx context.Context) {
	defer c.wg.Done()

	for {
		select {
		case <-ctx.Done():
			c.log.Debug().Msg("Receive loop cancelled")
			return
		default:
			// Set read timeout
			if c.config.ReadTimeout > 0 {
				deadline := time.Now().Add(c.config.ReadTimeout)
				if err := c.conn.SetReadDeadline(deadline); err != nil {
					c.handleConnectionLoss(err)
					return
				}
			}

			msg, err := c.conn.ReadMessage()
			if err != nil {
				if err == io.EOF {
					c.log.Debug().Msg("Server closed connection")
				} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
					if c.conn != nil {
						c.conn.UpdateLastSeen()
					}
					continue
				} else {
					c.log.Error().Err(err).Msg("Read error")
				}

				c.handleConnectionLoss(err)
				return
			}

			var verifiedID string
			if c.conn != nil {
				verifiedID = c.conn.GetAuthenticatedID()
			}

			c.log.Debug().
				Str("msg_type", fmt.Sprintf("%T", msg.Payload)).
				Str("sender_id", msg.SenderId).
				Str("verified_id", verifiedID).
				Msg("Message received")

			// Handle message
			if c.handler != nil {
				if _, err := c.handler(ctx, msg); err != nil {
					c.log.Error().
						Err(err).
						Str("msg_type", fmt.Sprintf("%T", msg.Payload)).
						Msg("Error handling message")
				}
			}
		}
	}
}

// Reconnect attempts to reconnect to the server
func (c *client) Reconnect(ctx context.Context) error {
	c.log.Info().
		Dur("delay", c.config.ReconnectDelay).
		Msg("Attempting reconnect")

	// Disconnect first
	if err := c.Disconnect(ctx); err != nil {
		c.log.Warn().Err(err).Msg("Error during disconnect before reconnect")
	}

	// Wait before reconnecting
	select {
	case <-time.After(c.config.ReconnectDelay):
	case <-ctx.Done():
		return ctx.Err()
	}

	if err := c.Connect(ctx, ""); err != nil {
		return err
	}

	c.autoReconnect.Store(true)
	return nil
}

// ConnectWithRetry connects with automatic retry logic using exponential backoff with jitter.
func (c *client) ConnectWithRetry(ctx context.Context, addr string, maxRetries int) error {
	var lastErr error
	delay := c.config.ReconnectDelay
	const maxDelay = time.Minute // Sensible max delay

	// For jitter. Using time.Now() is generally fine for this use case.
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			jitter := time.Duration(rng.Int63n(int64(delay / 2)))
			waitTime := delay + jitter

			c.log.Info().
				Int("attempt", attempt+1).
				Int("max_retries", maxRetries+1).
				Dur("delay", waitTime).
				Msg("Retrying connection")

			select {
			case <-time.After(waitTime):
			case <-ctx.Done():
				return ctx.Err()
			}
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}

		if err := c.Connect(ctx, addr); err == nil {
			return nil
		} else {
			lastErr = err
			c.log.Warn().
				Err(err).
				Int("attempt", attempt+1).
				Msg("Connection failed")
		}
	}

	return fmt.Errorf("failed to connect after %d attempts: %w", maxRetries+1, lastErr)
}

// GetStats returns client statistics
func (c *client) GetStats() map[string]interface{} {
	c.mu.RLock()
	connected := c.connected.Load()
	var connInfo transport.ConnectionInfo
	if c.conn != nil {
		connInfo = c.conn.Info()
	}
	c.mu.RUnlock()

	return map[string]interface{}{
		"client_id":     c.id,
		"connected":     connected,
		"server_addr":   c.config.ServerAddr,
		"bytes_read":    connInfo.BytesRead,
		"bytes_written": connInfo.BytesWritten,
		"connected_at":  connInfo.ConnectedAt,
		"last_seen":     connInfo.LastSeen,
	}
}

// SetReadTimeout sets the read timeout
func (c *client) SetReadTimeout(timeout time.Duration) {
	c.config.ReadTimeout = timeout
}

// SetWriteTimeout sets the write timeout
func (c *client) SetWriteTimeout(timeout time.Duration) {
	c.config.WriteTimeout = timeout
}

// SetReconnectDelay sets the reconnect delay
func (c *client) SetReconnectDelay(delay time.Duration) {
	c.config.ReconnectDelay = delay
}

// connectionMonitor monitors connection health and handles automatic reconnection
func (c *client) connectionMonitor(ctx context.Context) {
	defer c.wg.Done()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !c.connected.Load() && c.autoReconnect.Load() {
				c.log.Info().Msg("Connection not active; scheduling reconnect")
				c.triggerReconnectNow()
			}
		}
	}
}

func (c *client) handleConnectionLoss(err error) {
	c.mu.Lock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.mu.Unlock()

	wasConnected := c.connected.Swap(false)
	if wasConnected {
		c.metrics.RecordConnection("disconnected")
	}

	if !c.autoReconnect.Load() {
		c.log.Info().Err(err).Msg("Connection closed (auto-reconnect disabled)")
		return
	}

	c.log.Warn().Err(err).Msg("Connection to server lost")
	c.triggerReconnectNow()
}

func (c *client) triggerReconnectNow() {
	if !c.autoReconnect.Load() {
		return
	}
	if !c.reconnecting.CompareAndSwap(false, true) {
		return
	}

	go func() {
		defer c.reconnecting.Store(false)

		delay := c.config.ReconnectDelay
		if delay <= 0 {
			delay = time.Second
		}
		const maxDelay = time.Minute

		for c.autoReconnect.Load() {
			if err := c.Connect(context.Background(), ""); err == nil {
				c.log.Info().Msg("Successfully reconnected to server")
				return
			} else {
				c.log.Warn().
					Err(err).
					Dur("retry_in", delay).
					Msg("Reconnect attempt failed")
			}

			time.Sleep(delay)

			if delay < maxDelay {
				delay *= 2
				if delay > maxDelay {
					delay = maxDelay
				}
			}
		}
	}()
}
