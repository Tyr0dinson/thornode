package p2p

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
	"github.com/stretchr/testify/assert"
)

// MockNetworkStreamResetErr is a mock stream where Reset returns an error
type MockNetworkStreamResetErr struct {
	*MockNetworkStream
	resetErr bool
}

func (m *MockNetworkStreamResetErr) Reset() error {
	if m.resetErr {
		return errors.New("reset error")
	}
	return nil
}

func (m *MockNetworkStreamResetErr) SetReadDeadline(t time.Time) error {
	if m.errSetReadDeadLine {
		return errors.New("set read deadline error")
	}
	return nil
}

func (m *MockNetworkStreamResetErr) SetWriteDeadline(t time.Time) error {
	if m.errSetWriteDeadLine {
		return errors.New("set write deadline error")
	}
	return nil
}

func TestReadStreamMaxPayloadExceeded(t *testing.T) {
	ApplyDeadline.Store(false)
	s := NewMockNetworkStream()
	buf := make([]byte, LengthHeader)
	binary.LittleEndian.PutUint32(buf, MaxPayload+1)
	s.Buffer.Write(buf)

	_, err := ReadStreamWithBuffer(s)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceed max payload length")
}

func TestReadStreamShortRead(t *testing.T) {
	ApplyDeadline.Store(false)
	s := NewMockNetworkStream()
	buf := make([]byte, LengthHeader)
	binary.LittleEndian.PutUint32(buf, 100)
	s.Buffer.Write(buf)
	// Write only 50 bytes instead of 100
	s.Buffer.Write(bytes.Repeat([]byte("a"), 50))

	_, err := ReadStreamWithBuffer(s)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "short read")
}

func TestReadStreamSetDeadlineResetError(t *testing.T) {
	ApplyDeadline.Store(true)
	defer ApplyDeadline.Store(false)

	s := &MockNetworkStreamResetErr{
		MockNetworkStream: NewMockNetworkStream(),
		resetErr:          true,
	}
	s.errSetReadDeadLine = true

	_, err := ReadStreamWithBuffer(s)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "reset error")
}

func TestWriteStreamWithBuffer(t *testing.T) {
	ApplyDeadline.Store(false)

	t.Run("happy path", func(t *testing.T) {
		s := NewMockNetworkStream()
		err := WriteStreamWithBuffer([]byte("hello"), s)
		assert.NoError(t, err)
	})

	t.Run("set write deadline error with reset error", func(t *testing.T) {
		ApplyDeadline.Store(true)
		defer ApplyDeadline.Store(false)

		s := &MockNetworkStreamResetErr{
			MockNetworkStream: NewMockNetworkStream(),
			resetErr:          true,
		}
		s.errSetWriteDeadLine = true

		err := WriteStreamWithBuffer([]byte("hello"), s)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "reset error")
	})

	t.Run("set write deadline error without reset error", func(t *testing.T) {
		ApplyDeadline.Store(true)
		defer ApplyDeadline.Store(false)

		s := NewMockNetworkStream()
		s.errSetWriteDeadLine = true

		err := WriteStreamWithBuffer([]byte("hello"), s)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "you asked for it")
	})
}

func TestReadStreamWithBufferWithContext(t *testing.T) {
	ApplyDeadline.Store(false)

	t.Run("happy path", func(t *testing.T) {
		s := NewMockNetworkStream()
		input := []byte("hello world")
		err := WriteStreamWithBuffer(input, s)
		assert.NoError(t, err)

		ctx := context.Background()
		data, err := ReadStreamWithBufferWithContext(ctx, s)
		assert.NoError(t, err)
		assert.Equal(t, input, data)
	})

	t.Run("context cancelled", func(t *testing.T) {
		// Use a stream that blocks on read (empty stream)
		s := NewMockNetworkStream()
		// Write header indicating we need 100 bytes but provide none
		buf := make([]byte, LengthHeader)
		binary.LittleEndian.PutUint32(buf, 100)
		s.Buffer.Write(buf)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		_, err := ReadStreamWithBufferWithContext(ctx, s)
		assert.Error(t, err)
		assert.Equal(t, context.Canceled, err)
	})

	t.Run("context deadline", func(t *testing.T) {
		ApplyDeadline.Store(true)
		defer ApplyDeadline.Store(false)

		s := NewMockNetworkStream()
		input := []byte("test data")
		err := WriteStreamWithBuffer(input, s)
		assert.NoError(t, err)

		// Use a context with a deadline far in the future so it doesn't expire
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		data, err := ReadStreamWithBufferWithContext(ctx, s)
		assert.NoError(t, err)
		assert.Equal(t, input, data)
	})

	t.Run("max payload exceeded", func(t *testing.T) {
		s := NewMockNetworkStream()
		buf := make([]byte, LengthHeader)
		binary.LittleEndian.PutUint32(buf, MaxPayload+1)
		s.Buffer.Write(buf)

		ctx := context.Background()
		_, err := ReadStreamWithBufferWithContext(ctx, s)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "exceed max payload length")
	})

	t.Run("short read", func(t *testing.T) {
		s := NewMockNetworkStream()
		buf := make([]byte, LengthHeader)
		binary.LittleEndian.PutUint32(buf, 100)
		s.Buffer.Write(buf)
		s.Buffer.Write(bytes.Repeat([]byte("a"), 50))

		ctx := context.Background()
		_, err := ReadStreamWithBufferWithContext(ctx, s)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "short read")
	})

	t.Run("set read deadline error with reset error", func(t *testing.T) {
		ApplyDeadline.Store(true)
		defer ApplyDeadline.Store(false)

		s := &MockNetworkStreamResetErr{
			MockNetworkStream: NewMockNetworkStream(),
			resetErr:          true,
		}
		s.errSetReadDeadLine = true

		ctx := context.Background()
		_, err := ReadStreamWithBufferWithContext(ctx, s)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "reset error")
	})

	t.Run("header read error", func(t *testing.T) {
		s := NewMockNetworkStream()
		// Write fewer than LengthHeader bytes
		s.Buffer.Write([]byte{0x01})

		ctx := context.Background()
		_, err := ReadStreamWithBufferWithContext(ctx, s)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "error in read the message head")
	})
}

func TestWriteStreamWithBufferWithContext(t *testing.T) {
	ApplyDeadline.Store(false)

	t.Run("happy path", func(t *testing.T) {
		s := NewMockNetworkStream()
		ctx := context.Background()
		err := WriteStreamWithBufferWithContext(ctx, []byte("hello"), s)
		assert.NoError(t, err)

		// Verify we can read it back
		data, err := ReadStreamWithBuffer(s)
		assert.NoError(t, err)
		assert.Equal(t, []byte("hello"), data)
	})

	t.Run("context cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		// Use a stream that will block on write
		s := &slowStream{MockNetworkStream: NewMockNetworkStream(), writeDelay: time.Second}
		err := WriteStreamWithBufferWithContext(ctx, []byte("hello"), s)
		assert.Error(t, err)
		assert.Equal(t, context.Canceled, err)
	})

	t.Run("with deadline from context", func(t *testing.T) {
		ApplyDeadline.Store(true)
		defer ApplyDeadline.Store(false)

		s := NewMockNetworkStream()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		err := WriteStreamWithBufferWithContext(ctx, []byte("test"), s)
		assert.NoError(t, err)
	})

	t.Run("set write deadline error with reset error", func(t *testing.T) {
		ApplyDeadline.Store(true)
		defer ApplyDeadline.Store(false)

		s := &MockNetworkStreamResetErr{
			MockNetworkStream: NewMockNetworkStream(),
			resetErr:          true,
		}
		s.errSetWriteDeadLine = true

		ctx := context.Background()
		err := WriteStreamWithBufferWithContext(ctx, []byte("hello"), s)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "reset error")
	})
}

// slowStream is a mock stream that introduces a delay on write
type slowStream struct {
	*MockNetworkStream
	writeDelay time.Duration
}

func (s *slowStream) Write(p []byte) (int, error) {
	time.Sleep(s.writeDelay)
	return s.MockNetworkStream.Write(p)
}

func (s *slowStream) Reset() error {
	return nil
}

func TestStreamMgrAddMultiple(t *testing.T) {
	sm := NewStreamMgr()

	// Add multiple streams with the same ID
	s1 := NewMockNetworkStream()
	s2 := NewMockNetworkStream()
	sm.AddStream("test", s1)
	sm.AddStream("test", s2)
	assert.Len(t, sm.unusedStreams["test"], 2)

	// Add unknown stream
	s3 := NewMockNetworkStream()
	sm.AddStream(StreamUnknown, s3)
	assert.Len(t, sm.unusedStreams[StreamUnknown], 1)

	// Release should clear both "test" and "UNKNOWN" streams
	sm.ReleaseStream("test")
	_, ok := sm.unusedStreams["test"]
	assert.False(t, ok)
	_, ok = sm.unusedStreams[StreamUnknown]
	assert.False(t, ok)
}

func TestNewStreamMgr(t *testing.T) {
	sm := NewStreamMgr()
	assert.NotNil(t, sm)
	assert.NotNil(t, sm.unusedStreams)
	assert.NotNil(t, sm.streamLocker)
}

func TestReadStreamWithBufferDisabledDeadline(t *testing.T) {
	ApplyDeadline.Store(false)
	s := NewMockNetworkStream()
	input := []byte("test message")
	err := WriteStreamWithBuffer(input, s)
	assert.NoError(t, err)

	data, err := ReadStreamWithBuffer(s)
	assert.NoError(t, err)
	assert.Equal(t, input, data)
}

func TestReadStreamEmptyHeader(t *testing.T) {
	ApplyDeadline.Store(false)
	s := NewMockNetworkStream()
	// empty buffer - no header
	_, err := ReadStreamWithBuffer(s)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "error in read the message head")
}

// alwaysErrWriteStream always fails on Write - since bufio.Writer buffers,
// the error shows up on Flush
type alwaysErrWriteStream struct {
	*MockNetworkStream
}

func (e *alwaysErrWriteStream) Write(p []byte) (int, error) {
	return 0, errors.New("write error")
}

func TestWriteStreamWithBufferFlushError(t *testing.T) {
	ApplyDeadline.Store(false)

	// alwaysErrWriteStream fails on the underlying Write, which means
	// bufio.Writer.Flush() will fail
	s := &alwaysErrWriteStream{MockNetworkStream: NewMockNetworkStream()}

	err := WriteStreamWithBuffer([]byte("hello"), s)
	assert.Error(t, err)
}

func TestWriteStreamWithBufferWithContextFlushError(t *testing.T) {
	ApplyDeadline.Store(false)

	s := &alwaysErrWriteStream{MockNetworkStream: NewMockNetworkStream()}

	ctx := context.Background()
	err := WriteStreamWithBufferWithContext(ctx, []byte("hello"), s)
	assert.Error(t, err)
}

func TestReadStreamWithBufferWithContextDeadlineBeforeDefault(t *testing.T) {
	ApplyDeadline.Store(true)
	defer ApplyDeadline.Store(false)

	s := NewMockNetworkStream()
	input := []byte("test")
	err := WriteStreamWithBuffer(input, s)
	assert.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond*100)
	defer cancel()

	data, err := ReadStreamWithBufferWithContext(ctx, s)
	assert.NoError(t, err)
	assert.Equal(t, input, data)
}

func TestReadStreamWithBufferLimitWithContextRejectsBeforeBodyRead(t *testing.T) {
	ApplyDeadline.Store(false)
	s := NewMockNetworkStream()
	header := make([]byte, LengthHeader)
	binary.LittleEndian.PutUint32(header, 6)
	s.Buffer.Write(header)
	s.Buffer.Write(bytes.Repeat([]byte{1}, 6))

	_, err := ReadStreamWithBufferLimitWithContext(context.Background(), s, 5)

	assert.ErrorContains(t, err, "exceed protocol payload limit")
	assert.Equal(t, 6, s.Buffer.Len(), "the rejected control body must remain unread")
}

func TestReadStreamWithBufferLimitWithContextAcceptsBoundedControl(t *testing.T) {
	ApplyDeadline.Store(false)
	s := NewMockNetworkStream()
	input := []byte{1, 2, 3, 4, 5}
	header := make([]byte, LengthHeader)
	binary.LittleEndian.PutUint32(header, uint32(len(input)))
	s.Buffer.Write(header)
	s.Buffer.Write(input)

	data, err := ReadStreamWithBufferLimitWithContext(context.Background(), s, uint32(len(input)))

	assert.NoError(t, err)
	assert.Equal(t, input, data)
}

type cancelableBoundedReadStream struct {
	*MockNetworkStream
	readStarted chan struct{}
	reset       chan struct{}
	readOnce    sync.Once
	resetOnce   sync.Once
}

func newCancelableBoundedReadStream() *cancelableBoundedReadStream {
	return &cancelableBoundedReadStream{
		MockNetworkStream: NewMockNetworkStream(),
		readStarted:       make(chan struct{}),
		reset:             make(chan struct{}),
	}
}

func (s *cancelableBoundedReadStream) Read([]byte) (int, error) {
	s.readOnce.Do(func() { close(s.readStarted) })
	<-s.reset
	return 0, io.ErrClosedPipe
}

func (s *cancelableBoundedReadStream) Reset() error {
	s.resetOnce.Do(func() { close(s.reset) })
	return nil
}

func TestReadStreamWithBufferLimitWithContextHonorsCancellation(t *testing.T) {
	originalApplyDeadline := ApplyDeadline.Load()
	ApplyDeadline.Store(false)
	t.Cleanup(func() { ApplyDeadline.Store(originalApplyDeadline) })

	stream := newCancelableBoundedReadStream()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := ReadStreamWithBufferLimitWithContext(ctx, stream, 5)
		result <- err
	}()

	select {
	case <-stream.readStarted:
	case <-time.After(time.Second):
		t.Fatal("bounded stream read did not start")
	}
	cancel()

	select {
	case err := <-result:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("bounded stream read ignored context cancellation")
	}
}

func TestWriteStreamWithBufferWithContextDeadlineNotBeforeDefault(t *testing.T) {
	ApplyDeadline.Store(true)
	defer ApplyDeadline.Store(false)

	s := NewMockNetworkStream()
	ctx := context.Background()
	err := WriteStreamWithBufferWithContext(ctx, []byte("test"), s)
	assert.NoError(t, err)
}

// Ensure MockNetworkStream implements network.Stream interface
var _ network.Stream = (*MockNetworkStream)(nil)
