package router

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/hpatchsyntax"
	"golang.org/x/term"
)

// The simulation uses the production projection worker, authenticated event
// transport, durable captures, and terminal viewer. All source and evidence
// belong to one disposable directory, never the caller's workspace.
func runLiveDiffSimulation(ctx context.Context, stdin, stdout *os.File, speed float64, repeat bool) error {
	if math.IsNaN(speed) || speed < .1 || speed > 20 {
		return errors.New("simulation speed must be between 0.1 and 20")
	}
	if !term.IsTerminal(int(stdin.Fd())) || !term.IsTerminal(int(stdout.Fd())) {
		return errors.New("live diff simulation needs a terminal")
	}
	directory, err := os.MkdirTemp("", "mekugi-live-diff-simulation-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	workspace := filepath.Join(directory, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		return err
	}
	store, err := openMekugiReplayStore(filepath.Join(directory, "replay"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	broker := newLiveDiffBroker(ctx)
	broker.setScope(liveDiffScope{Workspaces: map[string]map[string]bool{workspace: {"simulation": true}}})
	store.liveDiff = broker.publish
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+liveDiffEventsPath, broker.serveEvents)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	broker.setEndpoint("http://" + listener.Addr().String() + liveDiffEventsPath)
	serverDone := make(chan struct{})
	go func() { defer close(serverDone); _ = server.Serve(listener) }()
	defer func() { cancel(); server.Close(); <-serverDone }()
	connection := filepath.Join(directory, "connection.json")
	if err := os.WriteFile(connection, mustMarshalJSON(broker.descriptor()), 0600); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		err := playLiveDiffSimulation(ctx, broker, store, workspace, speed, repeat)
		if err != nil && ctx.Err() == nil {
			broker.mu.Lock()
			broker.emitLocked(liveDiffEvent{Kind: "coverage", Status: "SIMULATION: failed: " + err.Error()})
			broker.mu.Unlock()
		}
		done <- err
	}()
	err = runLiveDiffTerminal(ctx, store, workspace, stdin, stdout, connection)
	cancel()
	playErr := <-done
	if errors.Is(playErr, context.Canceled) {
		playErr = nil
	}
	return errors.Join(err, playErr)
}

func playLiveDiffSimulation(ctx context.Context, broker *liveDiffBroker, store *mekugiReplayStore, workspace string, speed float64, repeat bool) error {
	pause := func(duration time.Duration) error {
		timer := time.NewTimer(time.Duration(float64(duration) / speed))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	status := func(text string) {
		broker.mu.Lock()
		broker.emitLocked(liveDiffEvent{Kind: "coverage", Status: "SIMULATION: " + text})
		broker.mu.Unlock()
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	// The fixture module is usable with go test, but never reads caller config.
	if err := root.WriteFile("go.mod", []byte("module example.com/live-diff-demo\n\ngo 1.27\n"), 0600); err != nil {
		return err
	}
	sequence := 0
	apply := func(script string) error {
		segments, _, err := hpatchsyntax.SplitShell(script)
		if err != nil {
			return err
		}
		for _, segment := range segments {
			if !segment.Shell {
				if err := mekugi.ValidateScriptSyntax(segment.Source); err != nil {
					return err
				}
			}
		}
		for _, segment := range segments {
			if segment.Shell {
				// Only fixed demonstration programs reach this path. They run
				// in the disposable fixture, never during preview projection.
				command := exec.CommandContext(ctx, "sh", "-c", segment.Source)
				command.Dir = workspace
				if output, err := command.CombinedOutput(); err != nil {
					return fmt.Errorf("simulation shell: %w: %s", err, output)
				}
				continue
			}
			result, err := mekugi.ApplyForHostRoot(ctx, root, segment.Source, "")
			if err != nil {
				return err
			}
			for i := range result.ReviewFiles {
				file := &result.ReviewFiles[i]
				if file.BeforePath != "" {
					file.BeforePath = filepath.Join(workspace, file.BeforePath)
				}
				if file.AfterPath != "" {
					file.AfterPath = filepath.Join(workspace, file.AfterPath)
				}
			}
			sequence++
			call := fmt.Sprintf("simulation-%d", sequence)
			id, err := store.reserveChange(ctx, workspace, "simulation", call)
			if err != nil {
				return err
			}
			if err := store.put(ctx, workspace, map[string]mekugiHistory{call: {
				Script: segment.Source, ChangeID: id, CorrelationID: call,
				Applied: true, ReviewFiles: result.ReviewFiles,
			}}); err != nil {
				return err
			}
		}
		return nil
	}
	for cycle := 1; ; cycle++ {
		// Repeat the same flows against a clean set of known fixture files while
		// retaining the session's actual capture history and current navigation.
		for _, name := range []string{"handler.go", "routes.go", "handler_test.go", "audit.go", "lifecycle.go", "notes.txt"} {
			if _, err := root.Stat(name); err == nil {
				if err := apply("in " + name + "\nrm\n"); err != nil {
					return err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := apply("new handler.go\ntype " + strconv.Quote(liveDiffSimulationHandler)); err != nil {
			return err
		}
		if err := pause(time.Second); err != nil {
			return err
		}
		steps := liveDiffSimulationSteps()
		for index, step := range steps {
			status(fmt.Sprintf("%d/%d · %s · cycle %d", index+1, len(steps), step.name, cycle))
			worker := startLiveDiffPreview(ctx, broker, workspace, "simulation", step.tool)
			var script strings.Builder
			streamErr := func() error {
				defer func() { worker.stop(); <-worker.done }()
				for _, fragment := range step.fragments {
					script.WriteString(fragment)
					worker.appendDelta(fragment)
					if err := pause(step.delay); err != nil {
						return err
					}
				}
				// Even --speed 20 leaves the asynchronous worker time to publish.
				return pause(max(750*time.Millisecond, time.Duration(speed*float64(3*liveDiffPreviewFrameDelay))))
			}()
			if streamErr != nil {
				return streamErr
			}
			if !step.interrupt && step.tool != mekugiRecoveryToolName {
				source := script.String()
				if step.tool == "shell" {
					source = "shell <<SIMULATION_SHELL\n" + source + "SIMULATION_SHELL\n"
				}
				err := apply(source)
				if step.reject {
					if err == nil {
						return errors.New("simulation rejection unexpectedly applied")
					}
					if ctx.Err() != nil {
						return ctx.Err()
					}
					status("rejected as expected · incomplete edit did not change files")
				} else if err != nil {
					return fmt.Errorf("%s: %w", step.name, err)
				}
			}
			if err := pause(time.Second); err != nil {
				return err
			}
		}
		status("finished · Go creation, edits, shell, rename/delete, rejection, interruption · q quit")
		if !repeat {
			return nil
		}
		if err := pause(2 * time.Second); err != nil {
			return err
		}
	}
}

type liveDiffSimulationStep struct {
	name      string
	tool      string
	fragments []string
	delay     time.Duration
	reject    bool
	interrupt bool
}

const liveDiffSimulationHandler = `package demo

import (
	"fmt"
	"net/http"
)

type Route struct {
	Path    string
	Status  int
	Message string
}

// Handler serves requests.
func Handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	for _, route := range routes {
		if r.URL.Path == route.Path {
			w.WriteHeader(route.Status)
			fmt.Fprintln(w, route.Message)
			return
		}
	}
	http.NotFound(w, r)
}
`

func liveDiffSimulationSteps() []liveDiffSimulationStep {
	var source strings.Builder
	source.WriteString("package demo\n\nimport \"net/http\"\n\nvar routes = []Route{\n")
	for row := 1; row <= 180; row++ {
		message := fmt.Sprintf("resource %03d is ready", row)
		if row == 60 {
			message = strings.Repeat("Unicode 界 é and quoted \"message\"; ", 12) + "WRAPPED_TIP"
		}
		fmt.Fprintf(&source, "\t{Path: \"/api/%03d\", Status: http.StatusOK, Message: %s},\n", row, strconv.Quote(message))
	}
	source.WriteString("}\n\nfunc RouteCount() int {\n\treturn len(routes)\n} // ROUTES_READY\n")
	fragments := []string{"new routes.go\ntype <<PATCH\n"}
	lines := strings.SplitAfter(source.String(), "\n")
	for _, line := range lines[:100] {
		middle := len(line) / 2
		fragments = append(fragments, line[:middle], line[middle:])
	}
	fragments = append(fragments, strings.Join(lines[100:], ""), "PAT", "CH\n")
	testSource := `package demo

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandler(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v2/001", nil)
	response := httptest.NewRecorder()
	Handler(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
}
`
	return []liveDiffSimulationStep{
		{name: "Go creation · syntax, Unicode wrapping, burst, centered final row", fragments: fragments, delay: 40 * time.Millisecond},
		{name: "partial target · keep the last useful preview", delay: 500 * time.Millisecond,
			fragments: []string{"in handler.go\ntype \"// Handler serves requests.\" \"// Handler serves demo requests.\"\n",
				"type \"http.MethodG", "et\" \"http.MethodPost\"\n"}},
		{name: "multi-file and distant hunks · follow the last changed file", delay: 250 * time.Millisecond,
			fragments: []string{"in routes.go\ntype \"\\\"/api/001\\\"\" \"\\\"/v2/001\\\"\"\n",
				"type \"\\\"/api/180\\\"\" \"\\\"/v2/180\\\"\"\nnew handler_test.go\ntype <<TEXT\n",
				testSource + "TE", "XT\n"}},
		{name: "shell-in-hpatch · streamed script, then actual segment captures", delay: 350 * time.Millisecond,
			fragments: []string{"new audit.go\ntype \"package demo\\n\\nconst phase = \\\"prepared\\\"\\n\"\n",
				"shell printf '%s\\n' 'checked fixture' > shell.log\n",
				"in audit.go\ntype \"prepared\" \"completed\"\n",
				"shell <<SHELL\ntest -f audit.go\nprintf '%s\\n' 'SHELL_TIP' >> shell.log\n", "SHELL\n"}},
		{name: "functions.shell · direct source, 7:3 layout", tool: "shell", delay: 350 * time.Millisecond,
			fragments: []string{"printf '%s\\n' 'standalone shell' > standalone.log\n",
				"test -f audit.go\n", "printf '%s\\n' 'FUNCTIONS_SHELL_TIP' >> standalone.log\n"}},
		{name: "append and rename · preserve file identity", delay: 300 * time.Millisecond,
			fragments: []string{"in audit.go\nadd EOF <<PATCH\n\nfunc AuditReady() bool {\n\treturn phase == \"completed\"\n}\nPATCH\n",
				"mv lifecycle.go\n"}},
		{name: "deletion · compact streamed marker", delay: 300 * time.Millisecond,
			fragments: []string{"in lifecycle.go\nrm\n"}},
		{name: "missing final newline", delay: 300 * time.Millisecond,
			fragments: []string{"new notes.txt\ntype \"Unicode 界 é without final newline\""}},
		{name: "rejection · keep applied state intact", delay: 500 * time.Millisecond, reject: true,
			fragments: []string{"in handler.go\ntype \"demo requests\" \"rejected requests\"\n",
				"type \"target that does not exist\" \"rejected\"\n"}},
		{name: "hpatch recovery · emitted correction overlay (display only)", tool: mekugiRecoveryToolName, delay: 350 * time.Millisecond,
			fragments: []string{"maple target \"", "demo requests\"\n", "maple value <<FIX\n", "RECOVERY_TIP\n", "FIX\n"}},
		{name: "interruption · preview only, no application", delay: 500 * time.Millisecond, interrupt: true,
			fragments: []string{"in handler.go\nadd EOF <<PATCH\n\nfunc InterruptedPreview() string {\n",
				"\treturn \"INTERRUPTED_TIP"}},
	}
}
