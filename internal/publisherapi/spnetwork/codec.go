package network

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"google.golang.org/protobuf/proto"
)

// Codec handles message encoding/decoding with pooling for performance.
type Codec struct {
	maxMessageSize int
	bufferPool     sync.Pool
}

// NewCodec creates a new codec with buffer pooling.
func NewCodec(maxMessageSize int) *Codec {
	return &Codec{
		maxMessageSize: maxMessageSize,
		bufferPool: sync.Pool{
			New: func() interface{} {
				return bufio.NewReaderSize(nil, 4096)
			},
		},
	}
}

// Encode marshals and adds length prefix.
func (c *Codec) Encode(msg proto.Message) ([]byte, error) {
	data, err := proto.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal: %w", err)
	}

	dataLen := len(data)
	if dataLen > c.maxMessageSize {
		return nil, fmt.Errorf("%w: message size %d exceeds max %d", ErrMessageTooLarge, dataLen, c.maxMessageSize)
	}

	if dataLen > int(^uint32(0)) {
		return nil, fmt.Errorf("message size %d exceeds uint32 range", dataLen)
	}

	result := make([]byte, 4+dataLen)
	binary.BigEndian.PutUint32(result[:4], uint32(dataLen))
	copy(result[4:], data)

	return result, nil
}

// Decode reads a length-prefixed message.
func (c *Codec) Decode(r io.Reader, msg proto.Message) error {
	lengthBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, lengthBuf); err != nil {
		return err
	}

	length := binary.BigEndian.Uint32(lengthBuf)
	if int(length) > c.maxMessageSize {
		return fmt.Errorf("%w: message size %d exceeds max %d", ErrMessageTooLarge, length, c.maxMessageSize)
	}

	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}

	if err := proto.Unmarshal(data, msg); err != nil {
		return fmt.Errorf("failed to unmarshal: %w", err)
	}

	return nil
}

// StreamWriter provides efficient writing with buffering.
type StreamWriter struct {
	w     io.Writer
	buf   *bufio.Writer
	codec *Codec
	mu    sync.Mutex
}

// NewStreamWriter creates a buffered writer.
func NewStreamWriter(w io.Writer, codec *Codec) *StreamWriter {
	return &StreamWriter{
		w:     w,
		buf:   bufio.NewWriterSize(w, 8192),
		codec: codec,
	}
}

// Write sends a message.
func (sw *StreamWriter) Write(msg proto.Message) error {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	data, err := sw.codec.Encode(msg)
	if err != nil {
		return err
	}

	if _, err := sw.buf.Write(data); err != nil {
		return err
	}

	return sw.buf.Flush()
}

// Close flushes and closes the writer.
func (sw *StreamWriter) Close() error {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.buf.Flush()
}
