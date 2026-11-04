// Package herdr adapts one bounded native panel request to the Session API.
package herdr

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"syscall"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

const maxResponse = 7 << 20

type Request struct {
	Method   string         `json:"method"`
	Instance string         `json:"instance,omitempty"`
	Thread   string         `json:"thread,omitempty"`
	Body     jsontext.Value `json:"body"`
}

// Run borrows the existing private server. It never starts a listener or runtime.
func Run(ctx context.Context, args []string, out, stderr io.Writer) int {
	flags := flag.NewFlagSet("herdr", flag.ContinueOnError)
	flags.SetOutput(stderr)
	pid := flags.Int("pid", 0, "native client PID")
	input := flags.String("request", "", "JSON request")
	if flags.Parse(args) != nil {
		return 2
	}
	if *pid <= 0 || flags.NArg() != 0 || *input == "" || len(*input) > 8192 {
		fmt.Fprintln(stderr, "mekugi herdr: --pid POSITIVE_PID --request JSON required")
		return 2
	}
	var request Request
	if json.Unmarshal([]byte(*input), &request, json.RejectUnknownMembers(true)) != nil || request.Body.Kind() != '{' {
		fmt.Fprintln(stderr, "mekugi herdr: invalid request JSON")
		return 2
	}
	switch request.Method {
	case "native/observe", "native/select", "native/release", "session/page", "content":
	default:
		fmt.Fprintln(stderr, "mekugi herdr: unsupported request method")
		return 2
	}
	fail := func(err error) int {
		message := err.Error()
		fmt.Fprintln(stderr, "mekugi herdr:", message[:min(len(message), 1024)])
		return 1
	}
	path, err := sessionapi.ControlSocketPath()
	if err != nil {
		return fail(err)
	}
	transport := sessionapi.ControlTransport(path)
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	rpc, err := sessionapi.OpenRPC(ctx, &http.Client{Transport: transport}, "http://mekugi"+sessionapi.NativeRPCPath, "")
	if err != nil {
		if failure, ok := errors.AsType[*sessionapi.RPCError](err); ok && failure.Data != nil && failure.Data.Status == 409 {
			return fail(fmt.Errorf("shared server requires control protocol %s; stop that server when its work is safe, launch the matching Mekugi build, and reconnect native terminals", sessionapi.ProtocolVersion))
		}
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			_, err = io.WriteString(out, "{\"available\":false}\n")
			if err == nil {
				return 0
			}
		}
		return fail(err)
	}
	defer rpc.Close()
	var native sessionapi.NativeFrontendRequest
	if request.Method != "session/page" && json.Unmarshal(request.Body, &native, json.RejectUnknownMembers(true)) != nil {
		fmt.Fprintln(stderr, "mekugi herdr: invalid native request body")
		return 2
	}
	native.PID, native.Instance = *pid, request.Instance
	method := request.Method
	if method == "content" {
		method = "native/content"
	}
	var result jsontext.Value
	if method == "session/page" {
		body, _ := json.Marshal(&sessionapi.NativeFrontendRequest{PID: *pid, Instance: request.Instance})
		var frontend sessionapi.NativeObservation
		if err = rpc.Call(ctx, "native/resolve", sessionapi.RPCParams{Body: body}, &frontend); err != nil {
			return fail(err)
		}
		if !frontend.Available {
			result = jsontext.Value(`{"available":false}`)
		} else {
			if request.Thread != frontend.Thread {
				return fail(errors.New("native thread changed; reopen observation"))
			}
			err = rpc.Call(ctx, method, sessionapi.RPCParams{Thread: request.Thread, Body: request.Body}, &result)
		}
	} else {
		body, _ := json.Marshal(&native)
		err = rpc.Call(ctx, method, sessionapi.RPCParams{Body: body}, &result)
	}
	if err != nil {
		return fail(err)
	}
	if len(result) > maxResponse {
		return fail(errors.New("response exceeds panel budget"))
	}
	if _, err = fmt.Fprintln(out, string(result)); err != nil {
		return 1
	}
	return 0
}
