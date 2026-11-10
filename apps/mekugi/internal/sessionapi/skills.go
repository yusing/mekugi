package sessionapi

import (
	"context"
	"errors"
	"net/http"
	"os"
)

// NativeSkillsSocketEnvironment passes the actual server endpoint to its
// workers, including invocations using a nondefault control socket.
const NativeSkillsSocketEnvironment = "MEKUGI_CONTROL_SOCKET"

type nativeSkillCatalog struct {
	Paths   map[string][]string `json:"paths"`
	Problem string              `json:"problem,omitempty"`
}

func (h *Hub) nativeSkillCatalog(ctx context.Context, thread string) (nativeSkillCatalog, *RPCError) {
	runtime, err := h.Requests(thread)
	if err != nil {
		return nativeSkillCatalog{}, rpcFailure(409, err.Error())
	}
	reader, ok := runtime.(interface {
		NativeSkillCatalog(context.Context, string) (map[string][]string, error)
	})
	if !ok {
		// Backends without a native catalog keep their managed skill reader.
		return nativeSkillCatalog{Paths: map[string][]string{}}, nil
	}
	paths, err := reader.NativeSkillCatalog(ctx, thread)
	result := nativeSkillCatalog{Paths: paths}
	if err != nil {
		result.Problem = err.Error()
	}
	return result, nil
}

// ReadNativeSkillCatalog connects an authenticated frontend's selected thread
// to its existing runtime. It never reads source bytes or starts a runtime.
func ReadNativeSkillCatalog(ctx context.Context, thread string) (map[string][]string, error) {
	if thread == "" {
		return nil, errors.New("native skills require an admitted thread")
	}
	path := os.Getenv(NativeSkillsSocketEnvironment)
	if path == "" {
		var err error
		path, err = ControlSocketPath()
		if err != nil {
			return nil, err
		}
	}
	transport := ControlTransport(path)
	defer transport.CloseIdleConnections()
	client, err := OpenRPC(ctx, &http.Client{Transport: transport}, "http://mekugi"+NativeRPCPath, "")
	if err != nil {
		return nil, err
	}
	defer client.Close()
	var result nativeSkillCatalog
	if err := client.Call(ctx, "source/skills", RPCParams{Thread: thread}, &result); err != nil {
		return nil, err
	}
	if result.Problem != "" {
		return result.Paths, errors.New(result.Problem)
	}
	return result.Paths, nil
}
