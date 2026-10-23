package launch

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

// RunServer owns execution lifetimes independently of connected clients.
func RunServer(ctx context.Context, args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("server", flag.ContinueOnError)
	flags.SetOutput(stderr)
	socket := flags.String("socket", "", "private control socket path")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "mekugi server: unexpected arguments")
		return 2
	}
	if *socket == "" {
		path, err := sessionapi.ControlSocketPath()
		if err != nil {
			fmt.Fprintln(stderr, "mekugi server: cannot locate control socket")
			return 1
		}
		*socket = path
	}
	stopProfiling, err := startProfiling(ctx, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "mekugi server:", err)
		return 1
	}
	defer func() {
		if err := stopProfiling(); err != nil {
			fmt.Fprintln(stderr, "mekugi server profiling:", err)
		}
	}()
	listener, err := sessionapi.ListenControl(*socket)
	if err != nil {
		fmt.Fprintln(stderr, "mekugi server:", err)
		return 1
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	hub := sessionapi.NewHub(ctx, &serverLauncher{})
	fmt.Fprintln(stderr, "Mekugi server ready:", *socket)
	err = serveSessionHub(ctx, listener, hub)
	cancel()
	if err != nil {
		fmt.Fprintln(stderr, "mekugi server:", err)
		return 1
	}
	return 0
}

// Keep the listener's exclusive lifetime lock until execution has joined. An
// HTTP server closes its listener during shutdown, so its cancellation follows
// runtime cancellation rather than racing it.
func serveSessionHub(ctx context.Context, listener net.Listener, hub *sessionapi.Hub) error {
	serving, stopServing := context.WithCancel(context.WithoutCancel(ctx))
	defer stopServing()
	joined := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		hub.Close()
		stopServing()
		close(joined)
	})
	err := sessionapi.Serve(serving, listener, sessionapi.Versioned(hub.Handler()))
	if stop() {
		hub.Close()
	} else {
		<-joined
	}
	return err
}
