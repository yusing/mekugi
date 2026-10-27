package httptransport_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/libraries/httptransport"
)

type responseBody struct {
	io.Reader
	close func() error
}

func (b responseBody) Close() error { return b.close() }

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestCancelOnCloseOwnsRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	closeErr := errors.New("source close")
	closed := 0
	body := httptransport.CancelOnClose(responseBody{
		Reader: bytes.NewBufferString("response"),
		close: func() error {
			closed++
			return closeErr
		},
	}, cancel)
	got, err := io.ReadAll(body)
	if err != nil || string(got) != "response" {
		t.Fatalf("read = %q, %v", got, err)
	}
	if ctx.Err() != nil || closed != 0 {
		t.Fatal("reading to EOF ended the request lifetime")
	}
	if err := body.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("close error = %v, want source error", err)
	}
	if closed != 1 || ctx.Err() != context.Canceled {
		t.Fatalf("close count = %d, request error = %v", closed, ctx.Err())
	}
}

func TestRestorePrefixPreservesBytesAndOriginalCloser(t *testing.T) {
	want := []byte{'\x00', '\xff', '\r', '\n', 'a', 'b', '\x80'}
	closed := 0
	closeErr := errors.New("original close")
	original := responseBody{Reader: bytes.NewReader(want), close: func() error {
		closed++
		return closeErr
	}}
	response := &http.Response{Body: original}
	remainder := bufio.NewReader(response.Body)
	prefix := make([]byte, 3)
	if _, err := io.ReadFull(remainder, prefix); err != nil {
		t.Fatal(err)
	}
	httptransport.RestorePrefix(response, prefix, remainder)
	got, err := io.ReadAll(response.Body)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("restored bytes = %x, %v; want %x", got, err, want)
	}
	if closed != 0 {
		t.Fatal("restoring or reading the prefix closed the source")
	}
	if err := response.Body.Close(); !errors.Is(err, closeErr) || closed != 1 {
		t.Fatalf("original closer: count=%d, error=%v", closed, err)
	}
}

func TestIdleReadTimeoutRetainsErrorsAndClosesOnce(t *testing.T) {
	for _, cancelOnClose := range []bool{false, true} {
		name := "active context"
		if cancelOnClose {
			name = "source close cancels context"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				closed := make(chan struct{})
				var closes atomic.Int32
				sourceErr := errors.New("source read")
				closeErr := errors.New("source close")
				body := httptransport.IdleRead(ctx, responseBody{
					Reader: readerFunc(func(p []byte) (int, error) {
						<-closed
						return copy(p, "partial"), sourceErr
					}),
					close: func() error {
						if closes.Add(1) == 1 {
							if cancelOnClose {
								cancel()
							}
							close(closed)
						}
						return closeErr
					},
				}, time.Second)
				start := time.Now()
				buffer := make([]byte, 32)
				n, err := body.Read(buffer)
				if string(buffer[:n]) != "partial" || !errors.Is(err, sourceErr) || !errors.Is(err, httptransport.ErrStreamIdleTimeout) {
					t.Fatalf("expired read = %q, %v", buffer[:n], err)
				}
				if errors.Is(err, context.Canceled) != cancelOnClose {
					t.Fatalf("cancellation identity = %v, want %v", err, cancelOnClose)
				}
				if elapsed := time.Since(start); elapsed != time.Second {
					t.Fatalf("idle expiration took %v, want 1s", elapsed)
				}
				if n, err := body.Read(buffer); n != 0 || !errors.Is(err, httptransport.ErrStreamIdleTimeout) {
					t.Fatalf("read after expiration = %d, %v", n, err)
				}
				for range 2 {
					if err := body.Close(); !errors.Is(err, closeErr) {
						t.Fatalf("close after expiration = %v", err)
					}
				}
				if closes.Load() != 1 {
					t.Fatalf("source closed %d times, want once", closes.Load())
				}
			})
		})
	}
}

func TestIdleReadResetExcludesConsumerBackpressure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		closed := false
		body := httptransport.IdleRead(t.Context(), responseBody{
			Reader: readerFunc(func(p []byte) (int, error) {
				time.Sleep(time.Second / 2)
				return copy(p, "x"), nil
			}),
			close: func() error { closed = true; return nil },
		}, time.Second)
		defer body.Close()
		for range 10 {
			var buf [1]byte
			if n, err := body.Read(buf[:]); n != 1 || err != nil || buf[0] != 'x' {
				t.Fatalf("read = %q, %v", buf[:n], err)
			}
			time.Sleep(2 * time.Second)
		}
		if closed {
			t.Fatal("consumer backpressure closed source")
		}
	})
}

func TestIdleReadWaitsForTimeoutCallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		closing, finish := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		body := httptransport.IdleRead(t.Context(), responseBody{
			Reader: readerFunc(func([]byte) (int, error) { <-closing; return 0, io.EOF }),
			close:  func() error { close(closing); <-finish; return nil },
		}, time.Second)
		go func() { _, err := body.Read(make([]byte, 1)); done <- err }()
		<-closing
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("read returned before timeout callback finished")
		default:
		}
		close(finish)
		if err := <-done; !errors.Is(err, httptransport.ErrStreamIdleTimeout) || !errors.Is(err, io.EOF) {
			t.Fatalf("read error = %v", err)
		}
	})
}

func BenchmarkIdleRead(b *testing.B) {
	body := httptransport.IdleRead(b.Context(), responseBody{
		Reader: readerFunc(func(p []byte) (int, error) { return len(p), nil }),
		close:  func() error { return nil },
	}, time.Minute)
	defer body.Close()
	var buf [16]byte
	if _, err := body.Read(buf[:]); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := body.Read(buf[:]); err != nil {
			b.Fatal(err)
		}
	}
}
