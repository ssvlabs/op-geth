package network

import "errors"

var (
	// ErrServerRunning is returned when trying to start an already running server.
	ErrServerRunning = errors.New("server already running")

	// ErrServerNotRunning is returned when trying to stop a non-running server.
	ErrServerNotRunning = errors.New("server not running")

	// ErrAlreadyConnected is returned when trying to connect an already connected client.
	ErrAlreadyConnected = errors.New("already connected")

	// ErrNotConnected is returned when trying to use a disconnected client.
	ErrNotConnected = errors.New("not connected")

	// ErrConnectionLimit is returned when the connection limit is reached.
	ErrConnectionLimit = errors.New("connection limit reached")

	// ErrMessageTooLarge is returned when a message exceeds the size limit.
	ErrMessageTooLarge = errors.New("message too large")
)
