package router

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// readPTYChunks owns the read buffer, not the reader or its process lifecycle.
// Cancellation interrupts a blocked delivery; callers still close the PTY to
// release a blocked read. Deliver final bytes before acting on a read error.
func readPTYChunks(ctx context.Context, reader io.Reader, bufferSize, capacity int) <-chan []byte {
	chunks := make(chan []byte, capacity)
	go func() {
		defer close(chunks)
		buffer := make([]byte, bufferSize)
		for {
			n, err := reader.Read(buffer)
			if n > 0 {
				select {
				case chunks <- bytes.Clone(buffer[:n]):
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return chunks
}

// synchronizedFrameBuffer preserves partial frame markers between reads.
// Next returns a borrowed slice; callers clone or convert it before delivery.
type synchronizedFrameBuffer struct {
	pending []byte
}

func (b *synchronizedFrameBuffer) Append(chunk []byte) {
	b.pending = append(b.pending, chunk...)
}

func (b *synchronizedFrameBuffer) Next() ([]byte, bool) {
	const endMarker = "\x1b[?2026l"
	end := bytes.Index(b.pending, []byte(endMarker))
	if end < 0 {
		return nil, false
	}
	end += len(endMarker)
	frame := b.pending[:end]
	b.pending = b.pending[end:]
	return frame, true
}

func TestPTYChunksRetainIndependentBuffers(t *testing.T) {
	chunks := readPTYChunks(t.Context(), strings.NewReader("abcdefgh"), 2, 4)
	var retained [][]byte
	for chunk := range chunks {
		retained = append(retained, chunk)
	}
	if got := string(bytes.Join(retained, nil)); got != "abcdefgh" {
		t.Fatalf("retained chunks = %q", got)
	}
}

type fixedPTYRead struct {
	data string
	err  error
}

func (r fixedPTYRead) Read(buffer []byte) (int, error) {
	return copy(buffer, r.data), r.err
}

func TestPTYChunksDeliverBytesBeforeReadError(t *testing.T) {
	chunks := readPTYChunks(t.Context(), fixedPTYRead{data: "final", err: io.EOF}, 8192, 1)
	if chunk, open := <-chunks; !open || string(chunk) != "final" {
		t.Fatalf("final chunk = %q, open %v", chunk, open)
	}
	if chunk, open := <-chunks; open {
		t.Fatalf("pump remained open after read error: %q", chunk)
	}
}

func TestPTYChunksCancellationReleasesDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	// An endless reader ensures EOF cannot hide missing cancellation handling.
	chunks := readPTYChunks(ctx, fixedPTYRead{data: "pending"}, 8192, 0)
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("cancelled pump remained blocked delivering a chunk")
		case _, open := <-chunks:
			// A ready receiver may race cancellation. Drain until closed.
			if !open {
				return
			}
		}
	}
}

func TestSynchronizedFrameBufferSplitAndCoalescedFrames(t *testing.T) {
	const first = "prefix\x1b[?2026hfirst\x1b[?2026l"
	const second = "\x1b[?2026hsecond\x1b[?2026l"
	for split := range len(first) {
		var buffer synchronizedFrameBuffer
		buffer.Append([]byte(first[:split]))
		if frame, ok := buffer.Next(); ok {
			t.Fatalf("split %d returned incomplete frame %q", split, frame)
		}
		buffer.Append([]byte(first[split:] + second + "unfinished"))
		for _, want := range []string{first, second} {
			if frame, ok := buffer.Next(); !ok || string(frame) != want {
				t.Fatalf("split %d frame = %q, %v; want %q", split, frame, ok, want)
			}
		}
		if frame, ok := buffer.Next(); ok || string(buffer.pending) != "unfinished" {
			t.Fatalf("split %d lost partial tail: frame %q, %v; pending %q", split, frame, ok, buffer.pending)
		}
	}
}
