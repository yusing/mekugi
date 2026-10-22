package launch

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/connector"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

// RunConnector serves remote human control without acquiring session ownership.
func RunConnector(ctx context.Context, args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("connector", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listen := flags.String("listen", "127.0.0.1:7778", "HTTP listen address; terminate HTTPS at a reverse proxy")
	socket := flags.String("socket", "", "private server control socket")
	tokenFile := flags.String("token-file", "", "private persistent bearer token file")
	var origins []string
	flags.Func("origin", "allowed UI origin (repeatable)", func(value string) error {
		origins = append(origins, value)
		return nil
	})
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "mekugi connector: unexpected positional arguments")
		return 2
	}
	if *socket == "" {
		var err error
		*socket, err = sessionapi.ControlSocketPath()
		if err != nil {
			fmt.Fprintln(stderr, "mekugi connector: cannot locate server control socket")
			return 1
		}
	}
	if *tokenFile == "" {
		*tokenFile = filepath.Join(filepath.Dir(*socket), "connector", "token")
	}
	c, err := connector.New(connector.Options{SocketPath: *socket, TokenFile: *tokenFile, Origins: origins})
	if err != nil {
		fmt.Fprintln(stderr, "mekugi connector:", err)
		return 1
	}
	defer func() {
		if err := c.Close(); err != nil {
			fmt.Fprintln(stderr, "mekugi connector:", err)
		}
	}()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(stderr, "mekugi connector: cannot listen on the requested address")
		return 1
	}
	server := &http.Server{Handler: c, ReadHeaderTimeout: 10 * time.Second}
	defer server.Close()
	rotations := make(chan os.Signal, 1)
	signal.Notify(rotations, syscall.SIGHUP)
	defer signal.Stop(rotations)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Fprintf(stderr, "Connector listening on %s. Token file: %s\n", listener.Addr(), *tokenFile)
	for {
		select {
		case <-ctx.Done():
			return 0
		case <-rotations:
			if err := c.Rotate(); err != nil {
				fmt.Fprintln(stderr, "mekugi connector:", err)
			} else {
				fmt.Fprintln(stderr, "Connector token rotated; previous connections closed.")
			}
		case <-done:
			fmt.Fprintln(stderr, "mekugi connector: HTTP listener stopped unexpectedly")
			return 1
		}
	}
}
