package router

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
)

// RunMCPBridge relays the dedicated child process's stdio to the router.
// It owns both stdio files and closes them when the router or host ends the connection.
func RunMCPBridge(ctx context.Context, socket string, stdin, stdout *os.File) error {
	if !filepath.IsAbs(socket) {
		return errors.New("MCP socket must be absolute")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	close := func() { connection.Close(); stdin.Close(); stdout.Close() }
	defer close()
	stop := context.AfterFunc(ctx, close)
	defer stop()
	sent := make(chan error, 1)
	go func() {
		_, err := io.Copy(connection, stdin)
		if err != nil {
			connection.Close()
		} else {
			err = connection.(*net.UnixConn).CloseWrite()
		}
		sent <- err
	}()
	_, err = io.Copy(stdout, connection)
	close()
	inputErr := <-sent
	if errors.Is(inputErr, os.ErrClosed) || errors.Is(inputErr, net.ErrClosed) {
		inputErr = nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.Join(err, inputErr)
}
