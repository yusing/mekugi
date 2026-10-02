package toolplugin

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const nativeResolverTimeout = 30 * time.Second
const nativeResolverDrain = time.Second

func resolverTimeoutError() error {
	return &symbolFailure{"resolver_timeout", "resolver exceeded the 30 s limit; retry with a narrower --workspace ROOT"}
}
func resolverStartError(name string, err error) error {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		hint := "expose " + name + " on the executor PATH"
		if name == "tsc" {
			hint = "expose TypeScript 7 tsc with --lsp support on the executor PATH"
		}
		return &symbolFailure{"dependency_unavailable", name + " is unavailable; " + hint}
	}
	return fmt.Errorf("cannot start %s: %w", name, err)
}
func nativeResolverCommand(ctx context.Context, root, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = root
	cmd.WaitDelay = nativeResolverDrain
	if !hostProcessGroupOwned(ctx) {
		ConfigureProcessGroup(cmd)
	}
	return cmd
}
func retireNativeResolver(cmd *exec.Cmd, ctx context.Context) {
	if cmd.Process == nil {
		return
	}
	if !hostProcessGroupOwned(ctx) {
		_ = cmd.Cancel()
	} else {
		_ = cmd.Process.Kill()
	}
}

type nativeRPCMessage struct {
	ID     jsontext.Value `json:"id,omitempty"`
	Method string         `json:"method,omitempty"`
	Params jsontext.Value `json:"params,omitempty"`
	Result jsontext.Value `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}
type nativeRPCEvent struct {
	message nativeRPCMessage
	err     error
}
type nativeLSP struct {
	input   io.Writer
	events  <-chan nativeRPCEvent
	nextID  int
	folders []map[string]string
}

func readNativeRPC(reader io.Reader, events chan<- nativeRPCEvent, stop <-chan struct{}) {
	defer close(events)
	r := bufio.NewReaderSize(reader, 8192)
	send := func(event nativeRPCEvent) bool {
		select {
		case events <- event:
			return true
		case <-stop:
			return false
		}
	}
	for {
		length := -1
		for {
			lineBytes, e := r.ReadSlice('\n')
			line := string(lineBytes)
			if e != nil {
				send(nativeRPCEvent{err: e})
				return
			}
			if len(line) > 8192 {
				send(nativeRPCEvent{err: errors.New("oversized LSP header")})
				return
			}
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if line == "" {
				break
			}
			key, value, ok := strings.Cut(line, ":")
			if ok && strings.EqualFold(key, "Content-Length") {
				length, e = strconv.Atoi(strings.TrimSpace(value))
				if e != nil {
					length = -1
				}
			}
		}
		if length < 0 || length > nativeRetainedBytes {
			send(nativeRPCEvent{err: errors.New("invalid LSP Content-Length")})
			return
		}
		data := make([]byte, length)
		if _, e := io.ReadFull(r, data); e != nil {
			send(nativeRPCEvent{err: e})
			return
		}
		var message nativeRPCMessage
		if e := json.Unmarshal(data, &message); e != nil {
			send(nativeRPCEvent{err: fmt.Errorf("invalid LSP message: %w", e)})
			return
		}
		if !send(nativeRPCEvent{message: message}) {
			return
		}
	}
}
func (l *nativeLSP) write(v any) error {
	data, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = fmt.Fprintf(l.input, "Content-Length: %d\r\n\r\n%s", len(data), data)
	return e
}
func (l *nativeLSP) notify(method string, params any) error {
	return l.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}
func (l *nativeLSP) request(ctx context.Context, method string, params any) (jsontext.Value, error) {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, resolverTimeoutError()
		}
		return nil, err
	}
	if pipe, ok := l.input.(interface{ SetWriteDeadline(time.Time) error }); ok {
		if deadline, ok := ctx.Deadline(); ok {
			_ = pipe.SetWriteDeadline(deadline)
			defer pipe.SetWriteDeadline(time.Time{})
		}
	}
	l.nextID++
	id := l.nextID
	if e := l.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); e != nil {
		return nil, e
	}
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, resolverTimeoutError()
			}
			return nil, ctx.Err()
		case event, ok := <-l.events:
			if !ok {
				return nil, errors.New("language server exited before replying")
			}
			if event.err != nil {
				return nil, fmt.Errorf("language server protocol: %w", event.err)
			}
			m := event.message
			if m.Method != "" {
				if len(m.ID) > 0 {
					var result any
					switch m.Method {
					case "workspace/configuration":
						var p struct {
							Items []any `json:"items"`
						}
						_ = json.Unmarshal(m.Params, &p)
						result = make([]any, len(p.Items))
					case "workspace/workspaceFolders":
						result = l.folders
					case "client/registerCapability", "window/workDoneProgress/create":
					default:
						if e := l.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "method not supported"}}); e != nil {
							return nil, e
						}
						continue
					}
					if e := l.write(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result}); e != nil {
						return nil, e
					}
				}
				continue
			}
			var responseID int
			if json.Unmarshal(m.ID, &responseID) != nil || responseID != id {
				continue
			}
			if m.Error != nil {
				return nil, errors.New(m.Error.Message)
			}
			if len(m.Result) == 0 {
				return nil, errors.New("LSP response has no result")
			}
			return m.Result, nil
		}
	}
}
func parseNativeLocations(raw jsontext.Value, mode string) ([]symbolLocation, error) {
	if string(raw) == "null" {
		return nil, nil
	}
	var values []jsontext.Value
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, err
		}
	} else if mode == "def" {
		values = []jsontext.Value{raw}
	} else {
		return nil, errors.New("invalid references response")
	}
	var out []symbolLocation
	for _, value := range values {
		type position struct {
			Line      *int `json:"line"`
			Character *int `json:"character"`
		}
		type span struct {
			Start *position `json:"start"`
			End   *position `json:"end"`
		}
		var v struct {
			URI         string `json:"uri"`
			TargetURI   string `json:"targetUri"`
			Range       *span  `json:"range"`
			TargetRange *span  `json:"targetRange"`
			Selection   *span  `json:"targetSelectionRange"`
		}
		if json.Unmarshal(value, &v) != nil {
			return nil, errors.New("invalid language-server location")
		}
		selected := v.Selection
		if selected == nil {
			selected = v.Range
		}
		if selected == nil {
			selected = v.TargetRange
		}
		uri := v.URI
		if uri == "" {
			uri = v.TargetURI
		}
		if uri == "" || selected == nil || selected.Start == nil || selected.End == nil || selected.Start.Line == nil || selected.Start.Character == nil || selected.End.Line == nil || selected.End.Character == nil {
			return nil, errors.New("invalid language-server location")
		}
		p := symbolRange{symbolPosition{*selected.Start.Line, *selected.Start.Character}, symbolPosition{*selected.End.Line, *selected.End.Character}}
		if p.Start.Line < 0 || p.Start.Character < 0 || p.End.Line < 0 || p.End.Character < 0 {
			return nil, errors.New("invalid language-server location")
		}
		path, err := symbolURIPath(uri)
		// A non-file URI is not a relative filesystem operand, even if a
		// same-spelled path happens to exist inside the workspace.
		out = append(out, symbolLocation{path: path, outside: err != nil, position: &p})
	}
	return out, nil
}

// Source: plugins/lsp.ts:119:279@[543de4f3] runLSPQueries
func runNativeLSP(ctx context.Context, root, resolver string, queries []*symbolQuery) {
	deadline, cancel := context.WithTimeout(ctx, nativeResolverTimeout)
	defer cancel()
	name, args := "tsc", []string{"--lsp", "--stdio"}
	if resolver == "python" {
		name, args = "pyright-langserver", []string{"--stdio"}
	}
	if resolver == "gopls" {
		name, args = "gopls", []string{"serve"}
	}
	failAll := func(err error) {
		for _, q := range queries {
			q.err = err
		}
	}
	cmd := nativeResolverCommand(deadline, root, name, args...)
	inputR, inputW, err := os.Pipe()
	if err != nil {
		failAll(err)
		return
	}
	defer inputR.Close()
	defer inputW.Close()
	outputR, outputW, err := os.Pipe()
	if err != nil {
		failAll(err)
		return
	}
	defer outputR.Close()
	defer outputW.Close()
	stderr := &boundedHostOutput{limit: nativeRetainedBytes + 1}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inputR, outputW, stderr
	if err := cmd.Start(); err != nil {
		failAll(resolverStartError(name, err))
		return
	}
	_ = inputR.Close()
	_ = outputW.Close()
	stopPipes := context.AfterFunc(deadline, func() { _ = inputW.Close(); _ = outputR.Close() })
	defer stopPipes()
	// Own the pipes explicitly: Wait must not close stdout before buffered replies
	// are dispatched. A descendant holding it open gets one second after exit.
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-done:
			select {
			case <-time.After(nativeResolverDrain):
				_ = outputR.Close()
			case <-stop:
			}
		case <-stop:
		}
	}()
	events := make(chan nativeRPCEvent, 16)
	go readNativeRPC(outputR, events, stop)
	lsp := nativeLSP{input: inputW, events: events, folders: []map[string]string{{"uri": symbolFileURI(root), "name": filepath.Base(root)}}}
	defer func() {
		grace := nativeResolverDrain
		if resolver == "gopls" {
			// Queries are finished. gopls shutdown can otherwise
			// wait for unrelated background diagnostics; do not charge that work
			// to a completed one-shot lookup.
			grace = 100 * time.Millisecond
		}
		shutdown, cancelShutdown := context.WithTimeout(context.Background(), grace)
		defer cancelShutdown()
		_, _ = lsp.request(shutdown, "shutdown", nil)
		_ = lsp.notify("exit", nil)
		_ = inputW.Close()
		select {
		case <-done:
		case <-shutdown.Done():
			retireNativeResolver(cmd, ctx)
			<-done
		}
		retireNativeResolver(cmd, ctx)
		if stderr.Len() > nativeRetainedBytes {
			for _, q := range queries {
				if q.err == nil {
					q.err = &symbolFailure{"output_limit", "resolver stderr exceeds the 16 MiB bound"}
				}
			}
			return
		}
		var diagnostic strings.Builder
		for line := range strings.Lines(stderr.String()) {
			trim := strings.TrimSpace(line)
			if trim != "context canceled" && trim != "error handling method 'exit': EOF" {
				diagnostic.WriteString(line)
			}
		}
		if len(queries) > 0 {
			queries[0].stderr = diagnostic.String()
		}
	}()
	params := map[string]any{"processId": os.Getpid(), "clientInfo": map[string]string{"name": "mekugi", "version": "1"}, "rootUri": symbolFileURI(root), "workspaceFolders": lsp.folders, "capabilities": map[string]any{"general": map[string]any{"positionEncodings": []string{"utf-16"}}, "workspace": map[string]any{"configuration": true, "workspaceFolders": true}, "textDocument": map[string]any{}}}
	if resolver == "gopls" {
		// This short-lived client only consumes semantic locations, not lint
		// diagnostics. Avoid competing staticcheck work without narrowing the
		// workspace, disabling tests, or changing reference resolution.
		params["initializationOptions"] = map[string]any{"staticcheck": false}
	}
	initialized, err := lsp.request(deadline, "initialize", params)
	if err != nil {
		failAll(err)
		return
	}
	var initializedData struct {
		Capabilities struct {
			PositionEncoding string `json:"positionEncoding"`
		} `json:"capabilities"`
	}
	if err = json.Unmarshal(initialized, &initializedData); err != nil {
		failAll(err)
		return
	}
	if encoding := initializedData.Capabilities.PositionEncoding; encoding != "" && encoding != "utf-16" {
		failAll(fmt.Errorf("unsupported language-server position encoding %q", encoding))
		return
	}
	if err = lsp.notify("initialized", map[string]any{}); err != nil {
		failAll(err)
		return
	}
	opened := map[string]bool{}
	for _, q := range queries {
		uri := symbolFileURI(q.file.path)
		if !opened[uri] {
			format := q.file.parsed.format
			language := format.Language
			if format.Kind == "json" {
				language = "json"
			}
			if format.JSX {
				language += "react"
			}
			err = lsp.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": uri, "languageId": language, "version": 1, "text": q.file.parsed.source}})
			if err != nil {
				q.err = err
				continue
			}
			opened[uri] = true
		}
		method := "textDocument/definition"
		params := map[string]any{"textDocument": map[string]string{"uri": uri}, "position": q.file.parsed.lines.position(q.offset)}
		if q.mode == "refs" {
			method = "textDocument/references"
			params["context"] = map[string]bool{"includeDeclaration": true}
		}
		raw, e := lsp.request(deadline, method, params)
		if e != nil {
			q.err = e
			continue
		}
		q.locations, q.err = parseNativeLocations(raw, q.mode)
	}
}
