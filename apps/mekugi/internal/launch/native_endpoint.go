package launch

import (
	"context"
	"errors"
	"reflect"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

var errNativeEndpointStopped = errors.New("native endpoint requested end")

// nativeEndpoint has only presentation/control lifetime. Sources are selected
// by the invocation's sole event loop, never by an endpoint goroutine.
type nativeEndpoint struct {
	cancel context.CancelFunc
	session *nativeSession
	api     *sessionapi.Server
	sources []nativeEventSource
	before  func() error
	after   func() error
	tick    func() error
	finish  func(error) error
	ending  bool
	waiters []nativeAttachRequest
}

type nativeEventSource struct {
	channel func() reflect.Value
	receive func(reflect.Value, bool) error
}

func nativeOn[T any](endpoint *nativeEndpoint, channel func() <-chan T, receive func(T, bool) error) {
	endpoint.sources = append(endpoint.sources, nativeEventSource{
		channel: func() reflect.Value { return reflect.ValueOf(channel()) },
		receive: func(value reflect.Value, ok bool) error {
			var item T
			if ok {
				item = value.Interface().(T)
			}
			return receive(item, ok)
		},
	})
}
