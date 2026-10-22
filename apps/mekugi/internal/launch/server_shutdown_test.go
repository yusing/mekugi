package launch

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

type shutdownLauncher struct{ started, stopping, release chan struct{} }

func (l *shutdownLauncher) StartOptions(context.Context) (sessionapi.StartOptions, error) {
	return sessionapi.StartOptions{}, nil
}
func (l *shutdownLauncher) PrepareInput(context.Context, string, sessionapi.InputRequest) (*sessionapi.PreparedInput, error) {
	return nil, nil
}
func (l *shutdownLauncher) Run(ctx context.Context, start sessionapi.Start, api *sessionapi.Server) error {
	close(l.started)
	request := <-api.Native().Requests()
	var frame sessionapi.NativeFrame
	frame.View.Version.Attachment, frame.View.Version.Revision = 1, 1
	frame.View.Info.Workspace = start.Workspace
	if err := api.Native().Publish(request.Attachment, frame); err != nil {
		return err
	}
	request.Reply <- ""
	<-ctx.Done()
	close(l.stopping)
	<-l.release
	return ctx.Err()
}
func TestServerKeepsSocketOwnershipUntilRuntimeJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(directory, "control.sock")
	listener, err := sessionapi.ListenControl(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	launcher := &shutdownLauncher{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	hub := sessionapi.NewHub(ctx, launcher)
	done := make(chan error, 1)
	go func() { done <- serveSessionHub(ctx, listener, hub) }()
	defer func() {
		cancel()
		select {
		case <-launcher.release:
		default:
			close(launcher.release)
		}
	}()
	transport := sessionapi.ControlTransport(socket)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	connection, err := sessionapi.StartNative(t.Context(), client, "http://server", sessionapi.NativeStart{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Next(); err != nil {
		t.Fatal(err)
	}
	connection.Close()
	<-launcher.started
	cancel()
	<-launcher.stopping
	if other, err := sessionapi.ListenControl(socket); err == nil {
		other.Close()
		t.Error("server released its socket before joining execution")
	}
	close(launcher.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server shutdown did not finish")
	}
	replacement, err := sessionapi.ListenControl(socket)
	if err != nil {
		t.Fatal(err)
	}
	replacement.Close()
}
