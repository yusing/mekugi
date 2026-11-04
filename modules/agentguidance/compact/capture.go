package compact

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

const SnapshotPrefix = "Reload snapshot: mread "

// CaptureCommand uses the authenticated worker's existing retained-output
// delivery. Source bytes never pass through stock tool output windows.
func CaptureCommand(selection string) string { return "mreload " + commandargs.Quote(selection) }

type Snapshot struct {
	Selection  string
	Sources    []CapturedSource
	Completion *CapturedSource `json:",omitempty"`
}

type CapturedSource struct {
	Command  string
	Stdout   string
	Stderr   string
	ExitCode int
}

func (s CapturedSource) Text() string {
	body := s.Stdout
	if s.Command != "" {
		body = fmt.Sprintf("%s: exit_code=%d\n%s", s.Command, s.ExitCode, body)
	}
	if s.Stderr != "" {
		body += "\n" + s.Stderr
	}
	return body
}

func Capture(ctx context.Context, request command.Request, changes ChangeReader) (command.Output, error) {
	if len(request.Args) != 1 {
		return command.Output{}, errors.New("mreload requires one reload selection")
	}
	snapshot, err := ReadSources(ctx, request.Args[0], "", nil, changes)
	if err != nil {
		return command.Output{}, err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return command.Output{}, err
	}
	if len(data) > history.MaxRecordBytes {
		return command.Output{}, errors.New("reload selection exceeds retained record capacity; select smaller ranges")
	}
	return command.Output{Delivery: capturedDelivery{string(data)}}, nil
}

// ReadSources acquires the shared selection in an admitted runtime environment.
// The caller owns policy admission and durable context retention.
func ReadSources(ctx context.Context, selection, directory string, environment []string, changes ChangeReader) (Snapshot, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	budget := &captureBudget{remaining: history.MaxRecordBytes, cancel: cancel}
	sources, err := Sources(selection)
	if err != nil {
		return Snapshot{}, err
	}
	commands, err := ReadCommands(selection)
	if err != nil {
		return Snapshot{}, err
	}
	// Keep original selection indices and command identity for retained history.
	// Host-loaded instructions have no acquisition or result body.
	results := make([]CapturedSource, len(sources))
	var readers sync.WaitGroup
	slots := make(chan struct{}, 8)
	commandIndex := 0
	for i, source := range sources {
		if source.HostLoaded() {
			continue
		}
		script := commands[commandIndex]
		commandIndex++
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			readers.Wait()
			return Snapshot{}, errors.Join(budget.err, ctx.Err())
		}
		readers.Go(func() {
			defer func() { <-slots }()
			if source.Changes != nil {
				results[i] = changes.read(ctx, source, directory, script)
				budget.take(len(results[i].Stdout) + len(results[i].Stderr))
				return
			}
			// A native metadata placeholder identifies a managed skill. Resolve
			// it inside the admitted reader, preserving the selected source identity.
			if source.Skill != "" && source.Path != "" && len(source.Ranges) == 0 {
				path := source.Path
				if !filepath.IsAbs(path) {
					path = filepath.Join(directory, path)
				}
				_, err := os.Stat(filepath.Join(filepath.Dir(path), ".skills-mgr-placeholder"))
				if err == nil {
					data, _ := json.Marshal(struct {
						Sources []Source `json:"sources"`
					}{[]Source{{Skill: source.Skill}}})
					managed, _ := ReadCommands(string(data))
					script = managed[0]
				} else if !os.IsNotExist(err) {
					results[i] = CapturedSource{Command: script, Stderr: err.Error(), ExitCode: 1}
					budget.take(len(results[i].Stderr))
					return
				}
			}
			var stdout, stderr bytes.Buffer
			cmd := exec.CommandContext(ctx, "bash", "-c", script)
			cmd.Dir, cmd.Env = directory, environment
			cmd.Stdout, cmd.Stderr = captureWriter{&stdout, budget}, captureWriter{&stderr, budget}
			cmd.WaitDelay = time.Second
			code := 0
			if err := cmd.Run(); err != nil {
				code = 1
				if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.ExitCode() > 0 {
					code = exit.ExitCode()
				}
				if stderr.Len() == 0 {
					fmt.Fprintln(&stderr, err)
				}
			}
			results[i] = CapturedSource{script, stdout.String(), stderr.String(), code}
		})
	}
	readers.Wait()
	if budget.err != nil {
		return Snapshot{}, budget.err
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Selection: selection, Sources: results}, nil
}

type captureBudget struct {
	mu        sync.Mutex
	remaining int
	err       error
	cancel    context.CancelFunc
}

func (b *captureBudget) take(n int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	if n > b.remaining {
		b.err = errors.New("reload selection exceeds retained record capacity; select smaller ranges")
		b.cancel()
		return b.err
	}
	b.remaining -= n
	return nil
}

type captureWriter struct {
	buffer *bytes.Buffer
	budget *captureBudget
}

func (w captureWriter) Write(p []byte) (int, error) {
	if err := w.budget.take(len(p)); err != nil {
		return 0, err
	}
	return w.buffer.Write(p)
}

// ChangeReader borrows the admitted retained owner and caller scope. It never
// resolves storage or authority from selected IDs or workspace paths.
type ChangeReader struct {
	Service changes.Service
	Scope   changes.Scope
}

func (r ChangeReader) read(ctx context.Context, source Source, directory, script string) CapturedSource {
	result := CapturedSource{Command: script}
	fail := func(err error) CapturedSource {
		result.Stderr, result.ExitCode = "mchanges: "+err.Error()+"\n", 1
		return result
	}
	if source.Path != "" || source.Skill != "" || len(source.Ranges) != 0 || len(source.Changes) == 0 {
		return fail(errors.New("changes source requires only explicit IDs/ranges"))
	}
	if r.Service == nil || r.Scope.Store == nil || r.Scope.Namespace == "" || r.Scope.Thread == "" {
		return fail(errors.New("change storage is unavailable"))
	}
	if directory == "" {
		var err error
		directory, err = os.Getwd()
		if err != nil {
			return fail(err)
		}
	}
	for _, ref := range source.Changes {
		if strings.HasPrefix(ref, "-") {
			return fail(errors.New("changes source requires only explicit IDs/ranges"))
		}
	}
	query, err := r.Service.Parse(source.Changes, directory)
	if err != nil {
		return fail(err)
	}
	if query.Mine || len(query.Paths) != 0 || query.View != "net" {
		return fail(errors.New("changes source requires only explicit IDs/ranges"))
	}
	result.Stdout, err = r.Service.Read(ctx, r.Scope, query)
	if err != nil {
		return fail(err)
	}
	return result
}

type capturedDelivery struct{ data string }

func (capturedDelivery) ReferenceCount() int { return 1 }

func (d capturedDelivery) Prepare(refs []string) (command.PreparedDelivery, error) {
	if len(refs) != 1 {
		return command.PreparedDelivery{}, errors.New("reload snapshot requires one reference")
	}
	return command.PreparedDelivery{
		Referenced:   command.Output{Stdout: SnapshotPrefix + refs[0] + "\n"},
		Unreferenced: command.Output{Stderr: "CONTENT NOT ATTACHED: reload snapshot publication failed\n", ExitCode: 1},
		Recovery:     []command.RecoveryOutput{{Stdout: d.data}},
	}, nil
}
