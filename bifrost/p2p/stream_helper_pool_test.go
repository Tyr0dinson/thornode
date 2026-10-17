package p2p

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/network"
)

// The reference* functions are verbatim copies of the original stream
// helpers, which wrapped every stream in a new bufio reader/writer.

func referenceReadStreamWithBuffer(stream network.Stream) ([]byte, error) {
	if ApplyDeadline.Load() {
		if err := stream.SetReadDeadline(time.Now().Add(TimeoutReadPayload)); err != nil {
			if errReset := stream.Reset(); errReset != nil {
				return nil, errReset
			}
			return nil, err
		}
	}
	streamReader := bufio.NewReader(stream)
	lengthBytes := make([]byte, LengthHeader)
	n, err := io.ReadFull(streamReader, lengthBytes)
	if n != LengthHeader || err != nil {
		return nil, fmt.Errorf("error in read the message head: %w", err)
	}
	length := binary.LittleEndian.Uint32(lengthBytes)
	if length > MaxPayload {
		return nil, fmt.Errorf("payload length:%d exceed max payload length:%d", length, MaxPayload)
	}
	dataBuf := make([]byte, length)
	n, err = io.ReadFull(streamReader, dataBuf)
	if uint32(n) != length || err != nil {
		return nil, fmt.Errorf("short read err(%w), we would like to read: %d, however we only read: %d", err, length, n)
	}
	return dataBuf, nil
}

func referenceWriteStreamWithBuffer(msg []byte, stream network.Stream, resetDeadlineError bool) error {
	length := uint32(len(msg))
	// Fail fast on the sender: receivers enforce the same bound and reject
	// the frame after the bandwidth is already spent, attributing the error
	// to the wrong side.
	if length > MaxPayload {
		return fmt.Errorf("payload length:%d exceed max payload length:%d", length, MaxPayload)
	}
	lengthBytes := make([]byte, LengthHeader)
	binary.LittleEndian.PutUint32(lengthBytes, length)
	if ApplyDeadline.Load() {
		if err := stream.SetWriteDeadline(time.Now().Add(TimeoutWritePayload)); err != nil {
			if resetDeadlineError {
				if errReset := stream.Reset(); errReset != nil {
					return errReset
				}
			}
			return err
		}
	}
	streamWrite := bufio.NewWriter(stream)
	n, err := streamWrite.Write(lengthBytes)
	if n != LengthHeader || err != nil {
		return fmt.Errorf("fail to write head: %w", err)
	}
	n, err = streamWrite.Write(msg)
	if err != nil {
		return err
	}
	if uint32(n) != length {
		return fmt.Errorf("short write, we would like to write: %d, however we only write: %d", length, n)
	}
	err = streamWrite.Flush()
	if err != nil {
		return fmt.Errorf("fail to flush stream: %w", err)
	}
	return nil
}

func referenceReadStreamWithBufferWithContext(ctx context.Context, stream network.Stream) ([]byte, error) {
	// Determine deadline from context or use default
	deadline := time.Now().Add(TimeoutReadPayload)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}

	// Apply deadline to stream
	if ApplyDeadline.Load() {
		if err := stream.SetReadDeadline(deadline); err != nil {
			if errReset := stream.Reset(); errReset != nil {
				return nil, errReset
			}
			return nil, err
		}
	}

	// Buffered to guarantee the reader can always send even if the caller
	// has already returned on context cancellation.
	readDone := make(chan struct{}, 1)
	var dataBuf []byte
	var readErr error

	// Perform the read operation in a separate goroutine
	go func() {
		streamReader := bufio.NewReader(stream)
		lengthBytes := make([]byte, LengthHeader)
		n, err := io.ReadFull(streamReader, lengthBytes)
		if n != LengthHeader || err != nil {
			readErr = fmt.Errorf("error in read the message head: %w", err)
			readDone <- struct{}{}
			return
		}

		length := binary.LittleEndian.Uint32(lengthBytes)
		if length > MaxPayload {
			readErr = fmt.Errorf("payload length:%d exceed max payload length:%d", length, MaxPayload)
			readDone <- struct{}{}
			return
		}

		dataBuf = make([]byte, length)
		n, err = io.ReadFull(streamReader, dataBuf)
		if uint32(n) != length || err != nil {
			readErr = fmt.Errorf("short read err(%w), we would like to read: %d, however we only read: %d", err, length, n)
		}
		readDone <- struct{}{}
	}()

	// Wait for either read completion or context cancellation
	select {
	case <-readDone:
		return dataBuf, readErr
	case <-ctx.Done():
		// Context was canceled or timed out
		// Reset the stream to prevent resource leaks
		_ = stream.Reset()
		return nil, ctx.Err()
	}
}

func referenceWriteStreamWithBufferWithContext(ctx context.Context, msg []byte, stream network.Stream) error {
	if len(msg) > MaxPayload {
		return fmt.Errorf("payload length:%d exceed max payload length:%d", len(msg), MaxPayload)
	}
	// Determine deadline from context or use default
	deadline := time.Now().Add(TimeoutWritePayload)
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl
	}

	// Apply deadline to stream
	if ApplyDeadline.Load() {
		if err := stream.SetWriteDeadline(deadline); err != nil {
			if errReset := stream.Reset(); errReset != nil {
				return errReset
			}
			return err
		}
	}

	// Buffered to guarantee the writer can always send even if the caller
	// has already returned on context cancellation.
	writeDone := make(chan error, 1)

	// Perform the write operation in a separate goroutine
	go func() {
		streamWrite := bufio.NewWriter(stream)
		length := uint32(len(msg))
		lengthBytes := make([]byte, LengthHeader)
		binary.LittleEndian.PutUint32(lengthBytes, length)

		n, err := streamWrite.Write(lengthBytes)
		if n != LengthHeader || err != nil {
			writeDone <- fmt.Errorf("fail to write head: %w", err)
			return
		}

		n, err = streamWrite.Write(msg)
		if err != nil {
			writeDone <- err
			return
		}

		if uint32(n) != length {
			writeDone <- fmt.Errorf("short write, we would like to write: %d, however we only write: %d", length, n)
			return
		}

		err = streamWrite.Flush()
		if err != nil {
			writeDone <- fmt.Errorf("fail to flush stream: %w", err)
			return
		}

		writeDone <- nil
	}()

	// Wait for either write completion or context cancellation
	select {
	case err := <-writeDone:
		return err
	case <-ctx.Done():
		// Context was canceled or timed out
		// Reset the stream to prevent resource leaks
		_ = stream.Reset()
		return ctx.Err()
	}
}

// scriptedStream caps bytes per Read and fails Writes past a byte budget.
type scriptedStream struct {
	*MockNetworkStream
	readChunk      int // 0 = unlimited
	writeFailAfter int // <0 = never
	written        int
}

var errScriptedWrite = errors.New("scripted write failure")

func (s *scriptedStream) Read(p []byte) (int, error) {
	if s.readChunk > 0 && len(p) > s.readChunk {
		p = p[:s.readChunk]
	}
	return s.Buffer.Read(p)
}

func (s *scriptedStream) Write(p []byte) (int, error) {
	if s.writeFailAfter >= 0 && s.written+len(p) > s.writeFailAfter {
		n := max(s.writeFailAfter-s.written, 0)
		s.written += n
		s.Buffer.Write(p[:n])
		return n, errScriptedWrite
	}
	s.written += len(p)
	return s.Buffer.Write(p)
}

type streamOp struct {
	write          bool
	ctx            bool
	payload        []byte
	input          []byte // stream contents for reads
	readChunk      int
	writeFailAfter int
}

func randomStreamOp(rng *rand.Rand) streamOp {
	sizes := []int{0, 1, 3, 4, 100, 4091, 4092, 4096, 4100, 8192, 70000}
	size := sizes[rng.Intn(len(sizes))]
	payload := make([]byte, size)
	rng.Read(payload)
	op := streamOp{write: rng.Intn(2) == 0, ctx: rng.Intn(3) == 0, payload: payload, writeFailAfter: -1}
	if rng.Intn(3) == 0 {
		op.readChunk = 1 + rng.Intn(5000)
	}
	if op.write {
		if rng.Intn(3) == 0 {
			op.writeFailAfter = rng.Intn(size + 8)
		}
		return op
	}
	// a frame, optionally followed by more frames (read-ahead) or truncated
	frame := make([]byte, LengthHeader, LengthHeader+size)
	binary.LittleEndian.PutUint32(frame, uint32(size))
	frame = append(frame, payload...)
	switch rng.Intn(5) {
	case 0:
		frame = frame[:rng.Intn(len(frame)+1)]
	case 1:
		frame = append(frame, frame...)
	case 2:
		binary.LittleEndian.PutUint32(frame, MaxPayload+1)
	}
	op.input = frame
	return op
}

func runStreamOp(op streamOp, pooled bool) (string, []byte) {
	s := &scriptedStream{MockNetworkStream: NewMockNetworkStream(), readChunk: op.readChunk, writeFailAfter: op.writeFailAfter}
	s.Buffer.Write(op.input)
	var res string
	switch {
	case op.write && !pooled && op.ctx:
		res = fmt.Sprint(referenceWriteStreamWithBufferWithContext(context.Background(), op.payload, s))
	case op.write && !pooled:
		res = fmt.Sprint(referenceWriteStreamWithBuffer(op.payload, s, true))
	case op.write && op.ctx:
		res = fmt.Sprint(WriteStreamWithBufferWithContext(context.Background(), op.payload, s))
	case op.write:
		res = fmt.Sprint(WriteStreamWithBuffer(op.payload, s))
	default:
		var data []byte
		var err error
		switch {
		case !pooled && op.ctx:
			data, err = referenceReadStreamWithBufferWithContext(context.Background(), s)
		case !pooled:
			data, err = referenceReadStreamWithBuffer(s)
		case op.ctx:
			data, err = ReadStreamWithBufferWithContext(context.Background(), s)
		default:
			data, err = ReadStreamWithBuffer(s)
		}
		res = fmt.Sprintf("%x|%v", data, err)
	}
	return res, s.Buffer.Bytes()
}

// TestPooledStreamBuffersMatchFresh interleaves reads and writes, many of them
// failing part-way, so pooled readers/writers carry read-ahead, unflushed
// bytes and sticky errors from one use into the next.
func TestPooledStreamBuffersMatchFresh(t *testing.T) {
	rng := rand.New(rand.NewSource(9)) //nolint:gosec // fixed seed: a reproducible differential test
	for i := 0; i < 5000; i++ {
		op := randomStreamOp(rng)
		wantRes, wantRest := runStreamOp(op, false)
		gotRes, gotRest := runStreamOp(op, true)
		if gotRes != wantRes {
			t.Fatalf("op %d (write=%v ctx=%v size=%d chunk=%d failAfter=%d): result %.200s, want %.200s",
				i, op.write, op.ctx, len(op.payload), op.readChunk, op.writeFailAfter, gotRes, wantRes)
		}
		if !bytes.Equal(gotRest, wantRest) {
			t.Fatalf("op %d (write=%v ctx=%v size=%d): stream contents differ (%d vs %d bytes)",
				i, op.write, op.ctx, len(op.payload), len(gotRest), len(wantRest))
		}
	}
}
