package router

import (
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type journalMCPReadInput struct {
	P     string `json:"p,omitempty"`
	Agent string `json:"agent,omitempty"`
	Depth *int   `json:"depth,omitempty"`
	View  string `json:"view,omitempty"`
}

type journalMCPMutateInput struct {
	Mutations []journalMutation `json:"mutations"`
}

func journalReadSchema() map[string]any {
	text := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"p":     text("Node path to read; omit for top-level nodes"),
			"agent": text("Proven ancestor or descendant agent path; `/root/` may be omitted. Omit to read your journal"),
			"depth": map[string]any{"type": "integer", "minimum": 0, "description": "Child levels to include; 0 returns only the selected or top-level nodes. Omit for all levels"},
			"view": map[string]any{
				"type": "string", "enum": []string{"combined", "own", "tasks", "outline"},
				"description": "combined (default) includes bodies and mounted agents; own excludes mounted agents; tasks keeps own task paths and states; outline keeps own node kinds and titles without bodies",
			},
		},
	}
}

func journalMutateSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"mutations"},
		"properties": map[string]any{"mutations": journalMutationsSchemaAt("#/properties/mutations")},
	}
}

// journalMCPDeclarations renders the input schemas for agent guidance. Codex
// defers these MCP tools and omits their declarations from the exec description.
func journalMCPDeclarations() string {
	mutations := journalMutationsSchemaAt("#/properties/mutations")
	var declarations strings.Builder
	declarations.WriteString("type JournalTask = " + journalTSType(mutations["$defs"].(map[string]any)["task"].(map[string]any), "") + ";\ntype JournalMutation =")
	for _, mutation := range mutations["items"].(map[string]any)["anyOf"].([]any) {
		declarations.WriteString("\n  | " + journalTSType(mutation.(map[string]any), ""))
	}
	declarations.WriteString(";\ntype JournalReadArgs = " + journalTSType(journalReadSchema(), "") + ";\n")
	declarations.WriteString("declare const tools: {\n  mcp__mekugi__journal_read(args: JournalReadArgs): Promise<CallToolResult>;\n")
	declarations.WriteString("  mcp__mekugi__journal_mutate(args: { mutations: JournalMutation[] }): Promise<CallToolResult>;\n};")
	return declarations.String()
}

// journalTSType covers the schema forms used by the journal tools. Objects
// with described properties render one commented property per line.
func journalTSType(schema map[string]any, indent string) string {
	if ref, ok := schema["$ref"].(string); ok {
		_, name, _ := strings.Cut(ref, "/$defs/")
		return "Journal" + strings.ToUpper(name[:1]) + name[1:]
	}
	if values, ok := schema["enum"].([]string); ok {
		quoted := make([]string, len(values))
		for i, value := range values {
			quoted[i] = strconv.Quote(value)
		}
		return strings.Join(quoted, " | ")
	}
	if options, ok := schema["anyOf"].([]any); ok {
		types := make([]string, len(options))
		for i, option := range options {
			types[i] = journalTSType(option.(map[string]any), indent)
		}
		return strings.Join(types, " | ")
	}
	switch schema["type"] {
	case "string":
		return "string"
	case "integer":
		return "number"
	case "array":
		return "Array<" + journalTSType(schema["items"].(map[string]any), indent) + ">"
	case "object":
		properties := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]string)
		var fields []string
		commented := false
		for _, name := range slices.Sorted(maps.Keys(properties)) {
			property := properties[name].(map[string]any)
			field := name
			if !slices.Contains(required, name) {
				field += "?"
			}
			field += ": " + journalTSType(property, indent+"  ") + ";"
			if description, ok := property["description"].(string); ok {
				field = "// " + description + "\n" + indent + "  " + field
				commented = true
			}
			fields = append(fields, field)
		}
		if commented {
			return "{\n" + indent + "  " + strings.Join(fields, "\n"+indent+"  ") + "\n" + indent + "}"
		}
		return "{ " + strings.Join(fields, " ") + " }"
	}
	return "unknown"
}

// journalMCPContext uses the same retained workspace and session owner as reads.
func journalMCPContext(ctx context.Context, proxy *mekugiProxy, meta mcp.Meta) (context.Context, string, string, func(), error) {
	thread, _ := meta["threadId"].(string)
	session, _ := meta["sessionId"].(string)
	if thread == "" || session == "" || proxy.replayStore == nil {
		return ctx, "", "", nil, errors.New("journal caller identity or storage is unavailable")
	}
	ctx, release, err := proxy.replayStore.beginSession(ctx, thread, session)
	if err != nil {
		return ctx, "", "", nil, err
	}
	var workspace string
	err = proxy.replayStore.scoped(ctx).locked(ctx, func() error {
		workspace, err = proxy.replayStore.retainedJournalWorkspace(thread, true)
		return err
	})
	if err != nil {
		release()
		return ctx, "", "", nil, err
	}
	return ctx, workspace, thread, release, nil
}

// newJournalMCPServer exposes the journal owner through a host-dispatched transport.
// Caller identity is supplied by Codex metadata, never by tool arguments.
func newJournalMCPServer(proxy *mekugiProxy) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "mekugi-journal", Version: "1"}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{},
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "journal_read", Description: "Read the caller's journal or a proven ancestor or descendant",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
		InputSchema: journalReadSchema(),
	}, func(ctx context.Context, request *mcp.CallToolRequest, input journalMCPReadInput) (*mcp.CallToolResult, any, error) {
		ctx, workspace, thread, release, err := journalMCPContext(ctx, proxy, request.Params.GetMeta())
		if err != nil {
			return nil, nil, err
		}
		defer release()
		nodes, err := proxy.readJournalTree(ctx, workspace, thread, input.Agent, input.P, input.Depth, input.View)
		if err != nil {
			return nil, nil, err
		}
		proxy.countJournalRead(ctx, workspace, thread, "", "read")
		return nil, map[string]any{"nodes": nodes}, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name:        "journal_mutate",
		Description: "Apply an atomic batch of journal plan, add, set, log, or remove operations; a final finish marker requests completion after the originating host result succeeds",
		// Updates and removals stay in retained journal history.
		Annotations: &mcp.ToolAnnotations{DestructiveHint: new(false), OpenWorldHint: new(false)},
		InputSchema: journalMutateSchema(),
	}, func(ctx context.Context, request *mcp.CallToolRequest, input journalMCPMutateInput) (*mcp.CallToolResult, any, error) {
		meta := request.Params.GetMeta()
		ctx, workspace, thread, release, err := journalMCPContext(ctx, proxy, meta)
		if err != nil {
			return nil, nil, err
		}
		defer release()
		call, _ := meta["callId"].(string)
		item, _ := meta["itemId"].(string)
		var turn struct {
			Thread string `json:"thread_id"`
			ID     string `json:"turn_id"`
		}
		encoded, err := json.Marshal(meta[codexTurnMetadataHeader])
		if err != nil || json.Unmarshal(encoded, &turn) != nil || turn.Thread != thread || turn.ID == "" || call == "" || item == "" {
			return nil, nil, errors.New("journal mutation requires host call, item, and turn identity")
		}
		mutations, finish, err := splitJournalFinish(input.Mutations)
		if err != nil {
			return nil, nil, err
		}
		receipt := fmt.Sprintf("runtime:mcp:%x", sha256.Sum256([]byte(turn.ID+"\x00"+call)))
		if finish {
			mutations = append(mutations, journalMutation{Op: "finish", finishTurn: turn.ID, finishItem: item})
		}
		paths, err := proxy.applyJournal(ctx, workspace, thread, receipt, mutations)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"paths": paths}, nil
	})
	return server
}

// startJournalMCP keeps journal access in the router. The private directory
// restricts access to this user's processes; no network endpoint is exposed.
func startJournalMCP(ctx context.Context, server *mcp.Server) (string, func() error, error) {
	// Unix socket paths are short even when TMPDIR is nested or aliased.
	directory, err := os.MkdirTemp("/tmp", "mekugi-journal-mcp-")
	if err != nil {
		return "", nil, err
	}
	socket := filepath.Join(directory, "journal.sock")
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
	return socket, func() error {
		cancel()
		err := listener.Close()
		workers.Wait()
		return errors.Join(err, os.RemoveAll(directory))
	}, nil
}

// RunJournalMCPBridge relays the dedicated child process's stdio to the router.
// It owns both stdio files and closes them when the router or host ends the connection.
func RunJournalMCPBridge(ctx context.Context, socket string, stdin, stdout *os.File) error {
	if !filepath.IsAbs(socket) {
		return errors.New("journal MCP socket must be absolute")
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
