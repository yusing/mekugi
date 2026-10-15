package mcptransport

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Source: internal/router/mcp_server.go:14:51@dbe80c66
// Start serves explicitly registered handlers on a private local endpoint. The private directory
// restricts access to this user's processes; no network endpoint is exposed.
func Start(ctx context.Context, server *mcp.Server) (string, func() error, error) {
	// Unix socket paths are short even when TMPDIR is nested or aliased.
	directory, err := os.MkdirTemp("/tmp", "mekugi-mcp-")
	if err != nil {
		return "", nil, err
	}
	socket := filepath.Join(directory, "mcp.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return "", nil, errors.Join(err, os.RemoveAll(directory))
	}
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	workers.Go(func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			workers.Go(func() {
				defer connection.Close()
				stop := context.AfterFunc(ctx, func() { connection.Close() })
				defer stop()
				_ = server.Run(ctx, &mcp.IOTransport{Reader: connection, Writer: connection})
			})
		}
	})
	stop := sync.OnceValue(func() error {
		cancel()
		err := listener.Close()
		workers.Wait()
		return errors.Join(err, os.RemoveAll(directory))
	})
	context.AfterFunc(ctx, func() { _ = stop() })
	return socket, stop, nil
}
