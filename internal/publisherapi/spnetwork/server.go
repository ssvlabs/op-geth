package network

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/internal/xt"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const (
	sharedPublisherClientID = "0"
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
	log      zerolog.Logger

	connections sync.Map // map[string]Connection
	writers     sync.Map // map[string]*StreamWriter

	running atomic.Bool
	wg      sync.WaitGroup
}

// NewServer creates a new server instance.
func NewServer(cfg ServerConfig, log zerolog.Logger) Server {
	return &server{
		cfg:   cfg,
		codec: NewCodec(cfg.MaxMessageSize),
		log:   log.With().Str("component", "server").Logger(),
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

	s.log.Info().
		Str("addr", s.cfg.ListenAddr).
		Str("sharedPublisherAddr", s.cfg.SharedPublisherAddr).
		Int("max_connections", s.cfg.MaxConnections).
		Msg("Server started")

	s.wg.Add(1)
	go s.acceptLoop(ctx)

	return nil
}

// SetHandler sets the message handler.
func (s *server) SetHandler(handler MessageHandler) {
	s.handler = handler
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
					s.log.Error().Err(err).Msg("Accept error")
				}
				return
			}

			// Check connection limit
			connCount := 0
			s.connections.Range(func(_, _ interface{}) bool {
				connCount++
				return true
			})

			if s.cfg.MaxConnections > 0 && connCount >= s.cfg.MaxConnections {
				s.log.Warn().
					Int("current", connCount).
					Int("max", s.cfg.MaxConnections).
					Msg(ErrConnectionLimit.Error())
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

	log := s.log.With().
		Str("conn_id", connID).
		Str("remote_addr", netConn.RemoteAddr().String()).
		Logger()

	// Store connection
	s.connections.Store(connID, conn)
	writer := NewStreamWriter(conn, s.codec)
	s.writers.Store(connID, writer)

	defer func() {
		conn.Close()
		s.connections.Delete(connID)
		s.writers.Delete(connID)
		writer.Close()
		log.Info().Msg("Connection closed")
	}()

	log.Info().Msg("New connection")

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
					log.Debug().Msg("Client disconnected")
				} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
					log.Debug().Msg("Read timeout")
				} else {
					log.Error().Err(err).Msg("Read error")
				}
				return
			}

			conn.UpdateLastSeen()

			if s.handler != nil {
				if err := s.handler(ctx, connID, &msg); err != nil {
					log.Error().Err(err).Msg("Handler error")
				}
			}
		}
	}
}

// Send sends a message to a shared publisher
func (s *server) SendToSP(_ context.Context, msg *xt.Message) error {
	writer, ok := s.writers.Load(sharedPublisherClientID)
	if !ok {
		return fmt.Errorf("client %s not found", sharedPublisherClientID)
	}

	return writer.(*StreamWriter).Write(msg)
}

// Stop gracefully stops the server.
func (s *server) Stop(ctx context.Context) error {
	if !s.running.CompareAndSwap(true, false) {
		return ErrServerNotRunning
	}

	s.log.Info().Msg("Stopping server")

	// Close listener
	if err := s.listener.Close(); err != nil {
		s.log.Error().Err(err).Msg("Failed to close listener")
	}

	// Close all connections
	s.connections.Range(func(key, value interface{}) bool {
		if conn, ok := value.(Connection); ok {
			conn.Close()
		}
		return true
	})

	// Wait for all goroutines with timeout
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.log.Info().Msg("Server stopped gracefully")
	case <-ctx.Done():
		s.log.Warn().Msg("Server stop timeout")
		return ctx.Err()
	}

	return nil
}
