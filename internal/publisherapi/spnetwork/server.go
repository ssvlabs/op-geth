package network

import (
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum/log"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/internal/xt"
	"github.com/google/uuid"
)

const (
	spClientID          = "0"
	spReconnectInterval = 10 * time.Second
)

// ServerConfig contains server configuration.
type ServerConfig struct {
	ListenAddr          string
	SharedPublisherAddr string
	ReadTimeout         time.Duration
	WriteTimeout        time.Duration
	MaxMessageSize      int
	MaxConnections      int
}

// server implements the Server interface
type server struct {
	cfg      ServerConfig
	listener net.Listener
	handler  MessageHandler
	codec    *Codec

	connections sync.Map // map[string]Connection
	writers     sync.Map // map[string]*StreamWriter

	running atomic.Bool
	wg      sync.WaitGroup

	spConnected atomic.Bool
	spCtxCancel context.CancelFunc
}

// NewServer creates a new server instance.
func NewServer(cfg ServerConfig) Server {
	return &server{
		cfg:   cfg,
		codec: NewCodec(cfg.MaxMessageSize),
	}
}

// Start starts the server.
func (s *server) Start(ctx context.Context) error {
	if !s.running.CompareAndSwap(false, true) {
		return ErrServerRunning
	}

	listener, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		s.running.Store(false)
		return fmt.Errorf("failed to listen: %w", err)
	}
	s.listener = listener

	log.Info("Server started", "addr", s.cfg.ListenAddr, "sharedPublisherAddr", s.cfg.SharedPublisherAddr, "max_connections", s.cfg.MaxConnections)

	s.wg.Add(1)
	go s.acceptLoop(ctx)

	if s.cfg.SharedPublisherAddr != "" {
		s.wg.Add(1)
		go s.maintainSPConnection(ctx)
	}

	return nil
}

// SetHandler sets the message handler.
func (s *server) SetHandler(handler MessageHandler) {
	s.handler = handler
}

func (s *server) maintainSPConnection(ctx context.Context) {
	defer s.wg.Done()

	log.Info("Starting shared publisher connection manager", "addr", s.cfg.SharedPublisherAddr)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			if err := s.connectToSP(ctx); err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Error("Shared publisher connection failed", "err", err)

				select {
				case <-ctx.Done():
					return
				case <-time.After(spReconnectInterval):
					// Continue to next iteration
				}
			}
		}
	}
}

// connectToSP establishes connection to shared publisher
func (s *server) connectToSP(ctx context.Context) error {
	// Create connection with timeout
	dialer := net.Dialer{Timeout: 5 * time.Second}
	netConn, err := dialer.DialContext(ctx, "tcp", s.cfg.SharedPublisherAddr)
	if err != nil {
		return fmt.Errorf("failed to dial shared publisher: %w", err)
	}

	// Create connection wrapper
	conn := NewConnection(netConn, spClientID)
	writer := NewStreamWriter(conn, s.codec)

	s.connections.Store(spClientID, conn)
	s.writers.Store(spClientID, writer)
	s.spConnected.Store(true)

	log.Info("Connected to shared publisher", "addr", s.cfg.SharedPublisherAddr)

	spCtx, cancel := context.WithCancel(ctx)
	s.spCtxCancel = cancel

	defer func() {
		cancel()
		s.spConnected.Store(false)
		s.connections.Delete(spClientID)
		s.writers.Delete(spClientID)
		writer.Close()
		conn.Close()
		log.Info("Shared publisher connection closed")
	}()

	<-spCtx.Done()
	return spCtx.Err()
}

// acceptLoop accepts new connections.
func (s *server) acceptLoop(ctx context.Context) {
	defer s.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// Accept with timeout
			s.listener.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second))

			netConn, err := s.listener.Accept()
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				if s.running.Load() {
					log.Error("Accept error", "err", err)
				}
				return
			}

			// Check connection limit (excluding shared publisher)
			connCount := 0
			s.connections.Range(func(key, _ interface{}) bool {
				if key.(string) != spClientID {
					connCount++
				}
				return true
			})

			if s.cfg.MaxConnections > 0 && connCount >= s.cfg.MaxConnections {
				log.Warn("Max connection warning", "current", connCount, "max", s.cfg.MaxConnections, "err", ErrConnectionLimit.Error())
				netConn.Close()
				continue
			}

			s.wg.Add(1)
			go s.handleConnection(ctx, netConn)
		}
	}
}

// handleConnection handles a client connection.
func (s *server) handleConnection(ctx context.Context, netConn net.Conn) {
	defer s.wg.Done()

	// Generate connection ID
	connID := uuid.New().String()
	conn := NewConnection(netConn, connID)

	// Store connection
	s.connections.Store(connID, conn)
	writer := NewStreamWriter(conn, s.codec)
	s.writers.Store(connID, writer)

	defer func() {
		conn.Close()
		s.connections.Delete(connID)
		s.writers.Delete(connID)
		writer.Close()
		log.Info("Connection closed", "remote_addr", netConn.RemoteAddr().String(), "connID", connID)
	}()

	log.Info("New connection", "remote_addr", netConn.RemoteAddr().String(), "connID", connID)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			if s.cfg.ReadTimeout > 0 {
				_ = conn.SetReadDeadline(time.Now().Add(s.cfg.ReadTimeout))
			}

			var msg xt.Message
			if err := s.codec.Decode(conn, &msg); err != nil {
				if err == io.EOF {
					log.Debug("Client disconnected", "remote_addr", netConn.RemoteAddr().String(), "connID", connID)
				} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
					log.Debug("Read timeout", "remote_addr", netConn.RemoteAddr().String(), "connID", connID)
				} else {
					log.Error("Read error", "remote_addr", netConn.RemoteAddr().String(), "connID", connID)
				}
				return
			}

			conn.UpdateLastSeen()

			if s.handler != nil {
				if _, err := s.handler(ctx, connID, &msg); err != nil {
					log.Error("Handler error", "remote_addr", netConn.RemoteAddr().String(), "connID", connID)
				}
			}
		}
	}
}

func (s *server) SendToSP(ctx context.Context, msg *xt.Message) error {
	return s.sendToSPWithRetry(ctx, msg, true)
}

func (s *server) sendToSPWithRetry(ctx context.Context, msg *xt.Message, allowRetry bool) error {
	if !s.spConnected.Load() {
		return fmt.Errorf("shared publisher not connected")
	}

	writer, ok := s.writers.Load(spClientID)
	if !ok {
		return fmt.Errorf("shared publisher writer not found")
	}

	err := writer.(*StreamWriter).Write(msg)
	if err != nil && allowRetry {
		log.Debug("Send to shared publisher failed, triggering reconnection")

		s.triggerSPReconnect()

		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}

		return s.sendToSPWithRetry(ctx, msg, false)
	}

	return err
}

func (s *server) triggerSPReconnect() {
	if s.spCtxCancel != nil {
		s.spCtxCancel()
	}
}

// Stop forcefully stops the server.
func (s *server) Stop(_ context.Context) error {
	if !s.running.CompareAndSwap(true, false) {
		return ErrServerNotRunning
	}

	if s.spCtxCancel != nil {
		s.spCtxCancel()
	}

	// Close listener
	if err := s.listener.Close(); err != nil {
		log.Error("Failed to close listener")
	}

	// Close all connections
	s.connections.Range(func(key, value interface{}) bool {
		if conn, ok := value.(Connection); ok {
			conn.Close()
		}
		return true
	})

	log.Info("Shared publisher server stopped")
	return nil
}
