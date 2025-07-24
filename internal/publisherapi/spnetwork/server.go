package network

import (
	"context"
	"fmt"
	"github.com/ethereum/go-ethereum/log"
	spcodec "github.com/ssvlabs/rollup-shared-publisher/pkg/codec"
	sperrors "github.com/ssvlabs/rollup-shared-publisher/pkg/errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	sptypes "github.com/ssvlabs/rollup-shared-publisher/pkg/proto"
)

// ServerConfig contains server configuration.
type ServerConfig struct {
	ListenAddr     string
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	MaxMessageSize int
	MaxConnections int
}

// server implements the Server interface
type server struct {
	cfg      ServerConfig
	listener net.Listener
	handler  MessageHandler
	codec    *spcodec.Codec

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
		codec: spcodec.NewCodec(cfg.MaxMessageSize),
	}
}

// Start starts the server.
func (s *server) Start(ctx context.Context) error {
	if !s.running.CompareAndSwap(false, true) {
		return sperrors.ErrServerRunning
	}

	listener, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		s.running.Store(false)
		return fmt.Errorf("failed to listen: %w", err)
	}
	s.listener = listener

	log.Info("Server started", "addr", s.cfg.ListenAddr, "max_connections", s.cfg.MaxConnections)

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
					log.Error("Accept error", "err", err)
				}
				return
			}

			// Check connection limit (excluding shared publisher)
			connCount := 0
			s.connections.Range(func(key, _ interface{}) bool {
				connCount++
				return true
			})

			if s.cfg.MaxConnections > 0 && connCount >= s.cfg.MaxConnections {
				log.Warn("Max connection warning", "current", connCount, "max", s.cfg.MaxConnections, "err", sperrors.ErrConnectionLimit.Error())
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
	writer := spcodec.NewStreamWriter(conn, s.codec)
	s.writers.Store(connID, writer)

	defer func() {
		conn.Close()
		s.connections.Delete(connID)
		s.writers.Delete(connID)
		writer.Close()
		log.Info("Connection closed", "remote_addr", netConn.RemoteAddr().String(), "connID", connID)
	}()

	log.Info("[SPNetwork server] New incoming connection", "remote_addr", netConn.RemoteAddr().String(), "connID", connID)

	for {
		select {
		case <-ctx.Done():
			return
		default:
			//if s.cfg.ReadTimeout > 0 {
			//	_ = conn.SetReadDeadline(time.Now().Add(s.cfg.ReadTimeout))
			//}

			var msg sptypes.Message
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
				if _, err := s.handler(ctx, &msg); err != nil {
					log.Error("Handler error", "remote_addr", netConn.RemoteAddr().String(), "connID", connID, "err", err)
				}
			}
		}
	}
}

// Stop forcefully stops the server.
func (s *server) Stop(_ context.Context) error {
	if !s.running.CompareAndSwap(true, false) {
		return sperrors.ErrServerNotRunning
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
