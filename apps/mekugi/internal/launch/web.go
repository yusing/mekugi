package launch

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/webui"
)

// RunWeb hosts browser assets without acquiring an execution or connector owner.
func RunWeb(ctx context.Context, args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("web", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listen := flags.String("listen", "127.0.0.1:7777", "HTTP listen address for browser assets")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "mekugi web: unexpected positional arguments")
		return 2
	}
	if ctx.Err() != nil {
		return 0
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(stderr, "mekugi web: cannot listen on the requested address")
		return 1
	}
	server := &http.Server{Handler: webui.Handler(), ReadHeaderTimeout: 10 * time.Second}
	defer server.Close()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Fprintf(stderr, "Web UI listening on http://%s\n", listener.Addr())
	select {
	case <-ctx.Done():
		server.Close()
		<-done
		return 0
	case <-done:
		fmt.Fprintln(stderr, "mekugi web: HTTP listener stopped unexpectedly")
		return 1
	}
}
