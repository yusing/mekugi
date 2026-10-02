package router

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type runtimeFrontendBinding struct {
	Runtime   string              `json:"runtime"`
	Workspace string              `json:"workspace"`
	Endpoint  ObservationEndpoint `json:"endpoint"`
}

func buildRuntimeToolRegistry(ctx context.Context, dataDirectory, runtimeDirectory, replayDirectory string, binding runtimeFrontendBinding) (*toolRegistry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	workspace, err := filepath.EvalSymlinks(binding.Workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve runtime frontend workspace: %w", err)
	}
	binding.Workspace = workspace
	if err := validateRuntimeFrontendBinding(binding); err != nil {
		return nil, err
	}
	if replayDirectory == "" {
		return nil, errors.New("runtime frontend storage is unavailable")
	}
	return buildToolRegistryWithRuntime(ctx, dataDirectory, false, runtimeDirectory, replayDirectory, &binding)
}

func validateRuntimeFrontendBinding(binding runtimeFrontendBinding) error {
	if binding.Runtime == "" || !filepath.IsAbs(binding.Workspace) || filepath.Clean(binding.Workspace) != binding.Workspace ||
		!filepath.IsAbs(binding.Endpoint.Socket) || binding.Endpoint.Token == "" {
		return errors.New("runtime frontend requires a runtime, absolute workspace and observation capability")
	}
	info, err := os.Stat(binding.Workspace)
	if err != nil {
		return fmt.Errorf("inspect runtime frontend workspace: %w", err)
	}
	if !info.IsDir() {
		return errors.New("runtime frontend workspace is not a directory")
	}
	return nil
}

// runtimeFrontendContext asks only for the authenticated root session. The
// endpoint cannot execute tools, and this identity cannot authorize own-agent
// change selection or journal operations.
func runtimeFrontendContext(ctx context.Context, binding runtimeFrontendBinding) (ObservationBinding, error) {
	if err := validateRuntimeFrontendBinding(binding); err != nil {
		return ObservationBinding{}, err
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", binding.Endpoint.Socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://local/frontend/context", nil)
	if err != nil {
		return ObservationBinding{}, err
	}
	request.Header.Set("Authorization", "Bearer "+binding.Endpoint.Token)
	response, err := client.Do(request)
	if err != nil {
		return ObservationBinding{}, fmt.Errorf("read runtime frontend context: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ObservationBinding{}, fmt.Errorf("runtime frontend context returned HTTP %d", response.StatusCode)
	}
	const maxReply = 8 << 10
	data, err := io.ReadAll(io.LimitReader(response.Body, maxReply+1))
	if err != nil {
		return ObservationBinding{}, fmt.Errorf("read runtime frontend context: %w", err)
	}
	if len(data) > maxReply {
		return ObservationBinding{}, errors.New("runtime frontend context exceeds 8 KiB")
	}
	var reply struct {
		Binding ObservationBinding `json:"binding"`
	}
	if err := json.Unmarshal(data, &reply, json.RejectUnknownMembers(true)); err != nil {
		return ObservationBinding{}, fmt.Errorf("decode runtime frontend context: %w", err)
	}
	root := reply.Binding
	if root.Runtime != binding.Runtime || root.Workspace != binding.Workspace || root.Session == "" || root.Agent != "" || root.Branch != "" {
		return ObservationBinding{}, errors.New("runtime frontend context does not match the root session binding")
	}
	return root, nil
}
