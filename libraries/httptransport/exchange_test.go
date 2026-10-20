package httptransport_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/libraries/httptransport"
)

func TestExchangeAdmissionDetachesOnlyStartup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		startup, cancelStartup := context.WithCancel(t.Context())
		defer cancelStartup()
		execution, cancelExecution := context.WithCancel(t.Context())
		defer cancelExecution()
		var request context.Context
		response, err := httptransport.Exchange(startup, execution, time.Minute, func(ctx context.Context) (*http.Response, error) {
			request = ctx
			return &http.Response{StatusCode: http.StatusOK, Body: responseBody{
				Reader: readerFunc(func(p []byte) (int, error) {
					if err := ctx.Err(); err != nil {
						return 0, err
					}
					return copy(p, "response"), nil
				}),
				close: func() error { return nil },
			}}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()

		cancelStartup()
		synctest.Wait()
		if err := request.Err(); err != nil {
			t.Fatalf("startup cancellation reached admitted request: %v", err)
		}
		buffer := make([]byte, 32)
		if n, err := response.Body.Read(buffer); err != nil || string(buffer[:n]) != "response" {
			t.Fatalf("read after startup cancellation = %q, %v", buffer[:n], err)
		}

		cancelExecution()
		if n, err := response.Body.Read(buffer); n != 0 || !errors.Is(err, context.Canceled) {
			t.Fatalf("read after execution cancellation = %d, %v", n, err)
		}
	})
}

func TestExchangeRejectionRemainsStartupCancelable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		startup, cancelStartup := context.WithCancel(t.Context())
		defer cancelStartup()
		reading := make(chan struct{})
		result := make(chan error, 1)
		closed := 0
		go func() {
			_, err := httptransport.Exchange(startup, t.Context(), time.Minute, func(ctx context.Context) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusBadGateway, Body: responseBody{
					Reader: readerFunc(func([]byte) (int, error) {
						close(reading)
						<-ctx.Done()
						return 0, ctx.Err()
					}),
					close: func() error { closed++; return nil },
				}}, nil
			})
			result <- err
		}()
		<-reading
		cancelStartup()
		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Fatalf("rejection read error = %v, want startup cancellation", err)
		}
		if closed != 1 {
			t.Fatalf("rejected source closed %d times, want once", closed)
		}
	})
}

func TestExchangeSendErrorPreservesIdentityAndCancelsRequest(t *testing.T) {
	want := &httptransport.SendError{Err: errors.New("send failed")}
	var request context.Context
	response, err := httptransport.Exchange(t.Context(), t.Context(), time.Minute, func(ctx context.Context) (*http.Response, error) {
		request = ctx
		return nil, want
	})
	if response != nil || err != want {
		t.Fatalf("exchange = %v, %v; want nil and original send error", response, err)
	}
	if err := request.Err(); err != context.Canceled {
		t.Fatalf("failed request context = %v, want canceled", err)
	}
}
