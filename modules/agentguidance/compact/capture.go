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
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

const SnapshotPrefix = "Reload snapshot: mread "

// CaptureCommand uses the authenticated worker's existing retained-output
// delivery. Source bytes never pass through stock tool output windows. Internal
// native metadata stays separate from the reference-string selection.
func CaptureCommand(selection string) string {
	if _, err := Sources(selection); err == nil {
		return "mreload " + commandargs.Quote(selection)
	}
	sources, err := StoredSources(selection)
	if err != nil {
		return "mreload " + commandargs.Quote(selection)
	}
	command := "mreload " + commandargs.Quote(Selection(sources))
	paths := make(map[string]string)
	for _, source := range sources {
		if source.Skill != "" && source.Path != "" {
			paths[source.Skill] = source.Path
		}
	}
	if len(paths) > 0 {
		data, _ := json.Marshal(paths, json.Deterministic(true))
		command = nativeSkillsEnvironment + "=" + commandargs.Quote(string(data)) + " " + command
	}
	return command
}

const nativeSkillsEnvironment = "MEKUGI_NATIVE_SKILLS"

type Snapshot struct {
	Selection string
	Sources   []CapturedSource
	// Skill paths are resolved native identities, never selectable source fields.
	NativeSkills map[string]string `json:",omitempty"`
	Completion   *CapturedSource   `json:",omitempty"`
	// Filtered completions retain a full bounded variant for context replacement.
	CompletionContext string          `json:",omitempty"`
	FullCompletion    *CapturedSource `json:",omitempty"`
}

func (s Snapshot) SelectedSources() ([]SourceIdentity, error) {
	sources, err := StoredSources(s.Selection)
	for i := range sources {
		if sources[i].Skill != "" && sources[i].Path == "" {
			sources[i].Path = s.NativeSkills[sources[i].Skill]
		}
	}
	return sources, err
}

// Text retains each source's reader identity, status and diagnostics.
func (s Snapshot) Text() string {
	var text strings.Builder
	for _, source := range s.Sources {
		text.WriteString(source.Text())
		text.WriteByte('\n')
	}
	return text.String()
}

// AttachmentText keeps source boundaries in native message snapshots. Failed
// reads and diagnostics remain ordinary evidence, not deduplication sources.
func (s Snapshot) AttachmentText() string {
	var text strings.Builder
	var bodies []attachments.SourceRange
	for _, source := range s.Sources {
		captured := source.Text()
		if source.ExitCode == 0 && source.Stdout != "" {
			start := text.Len() + len(captured) - len(source.Stdout)
			if source.Stderr != "" {
				start -= len(source.Stderr) + 1
			}
			bodies = append(bodies, attachments.SourceRange{Start: start, End: start + len(source.Stdout)})
		}
		text.WriteString(captured)
		text.WriteByte('\n')
	}
	return attachments.SourceRanges(text.String(), bodies)
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

// Readers borrows admitted acquisition owners. Skills returns native metadata
// only; Run, when supplied, retains the receiving runtime's read permissions.
type Readers struct {
	Changes ChangeReader
	Skills  func(context.Context) (map[string][]string, error)
	Run     SourceRunner
}

func (r Readers) Capture(ctx context.Context, request command.Request) (command.Output, error) {
	if len(request.Args) != 1 {
		return command.Output{}, errors.New("mreload requires one reload selection")
	}
	if _, err := Sources(request.Args[0]); err != nil {
		return command.Output{}, err
	}
	snapshot, err := r.Read(ctx, request.Args[0], "", nil)
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
	return (Readers{Changes: changes}).Read(ctx, selection, directory, environment)
}

func (r Readers) Read(ctx context.Context, selection, directory string, environment []string) (Snapshot, error) {
	return readSources(ctx, selection, directory, environment, r.Changes, r.Run, r.Skills)
}

// SourceRunner executes a reader under its receiving runtime's permissions.
// Output must be bounded by limit; an overflow must not look like a complete read.
type SourceRunner func(ctx context.Context, script string, limit int) (CapturedSource, error)

// ReadSourcesWithRunner shares selection, nesting and capture bounds with reloads,
// while leaving process execution and permissions with the receiving runtime.
func ReadSourcesWithRunner(ctx context.Context, selection, directory string, run SourceRunner) (Snapshot, error) {
	if run == nil {
		return Snapshot{}, errors.New("source runner is unavailable")
	}
	return (Readers{Run: run}).Read(ctx, selection, directory, nil)
}

func readSources(ctx context.Context, selection, directory string, environment []string, changes ChangeReader, run SourceRunner, catalog func(context.Context) (map[string][]string, error)) (Snapshot, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	budget := &captureBudget{remaining: history.MaxRecordBytes, cancel: cancel}
	sources, err := StoredSources(selection)
	if err != nil {
		return Snapshot{}, err
	}
	knownSkills := sync.OnceValues(func() (map[string]string, error) {
		var paths map[string]string
		vars := environment
		if vars == nil {
			vars = os.Environ()
		}
		for _, variable := range vars {
			if value, ok := strings.CutPrefix(variable, nativeSkillsEnvironment+"="); ok {
				if err := json.Unmarshal([]byte(value), &paths); err != nil {
					return nil, fmt.Errorf("invalid native skill identities: %w", err)
				}
			}
		}
		return paths, nil
	})
	nativeSkills := sync.OnceValues(func() (map[string][]string, error) {
		if catalog == nil {
			return nil, nil
		}
		return catalog(ctx)
	})
	resolve := func(source SourceIdentity) SourceIdentity {
		if source.Skill != "" && source.Path == "" {
			known, err := knownSkills()
			if err != nil {
				source.Problem = err.Error()
				return source
			}
			name, _, _ := strings.Cut(source.Skill, "/")
			source.Path = known[source.Skill]
			if source.Path == "" {
				source.Path = known[name]
			}
			if source.Path == "" {
				paths, err := nativeSkills()
				switch {
				case len(paths[name]) > 1:
					source.Problem = fmt.Sprintf("ambiguous native skill %q; select its file explicitly", name)
				case len(paths[name]) == 1:
					source.Path = paths[name][0]
				case err != nil:
					source.Problem = "native skill identity unavailable: " + err.Error()
				}
			}
		}
		return source
	}
	// Nested reads use the same admitted process environment and capture budget.
	acquire := func(source SourceIdentity, script string) (CapturedSource, bool) {
		managedSource := source.Skill != "" && source.Path == ""
		// A native metadata placeholder identifies a managed skill. Resolve
		// it inside the admitted reader, preserving the selected source identity.
		if source.Skill != "" && source.Path != "" {
			path := source.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(directory, path)
			}
			_, err := os.Stat(filepath.Join(filepath.Dir(path), ".skills-mgr-placeholder"))
			if err == nil {
				managedSource = true
				script = (SourceIdentity{Skill: source.Skill, Ranges: source.Ranges}).readCommand()
			} else if !os.IsNotExist(err) {
				result := CapturedSource{Command: script, Stderr: err.Error(), ExitCode: 1}
				budget.take(len(result.Stderr))
				return result, managedSource
			}
		}
		if run != nil {
			result, err := run(ctx, script, budget.remaining)
			result.Command = script
			if err != nil {
				result.ExitCode = 1
				result.Stderr += "\nCONTENT NOT ATTACHED: " + err.Error()
			}
			budget.take(len(result.Stdout) + len(result.Stderr))
			return result, managedSource
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
		return CapturedSource{script, stdout.String(), stderr.String(), code}, managedSource
	}
	type capturedFile struct {
		body, base, command string
		depth               int
	}
	var readSource func(SourceIdentity, int, map[string]capturedFile) CapturedSource
	readSource = func(source SourceIdentity, depth int, seen map[string]capturedFile) CapturedSource {
		source = resolve(source)
		if source.HostLoaded() {
			return CapturedSource{}
		}
		script := source.readCommand()
		if source.Changes != nil && run == nil {
			result := changes.read(ctx, source, directory, script)
			budget.take(len(result.Stdout) + len(result.Stderr))
			return result
		}
		path := source.readPath()
		if !filepath.IsAbs(path) {
			path = filepath.Join(directory, path)
		}
		key := source.Skill + "\x00" + filepath.Clean(path) + fmt.Sprint(source.Ranges)
		var result CapturedSource
		var body, base string
		if prior, ok := seen[key]; ok {
			if prior.depth <= depth {
				return result
			}
			result.Command, body, base = prior.command, prior.body, prior.base
		} else {
			var managed bool
			result, managed = acquire(source, script)
			if result.ExitCode != 0 {
				return result
			}
			body, base = result.Stdout, directory
			if source.Path != "" && !managed {
				base = filepath.Dir(path)
			}
		}
		seen[key] = capturedFile{body, base, result.Command, depth}
		if source.Changes != nil {
			return result // Captured diffs are evidence, not reference-bearing text.
		}

		for reference := range attachments.References(body) {
			if ctx.Err() != nil {
				return result
			}
			nested := resolve(sourceReference(reference))
			if nested.Path != "" {
				if !filepath.IsAbs(nested.Path) {
					nested.Path = filepath.Join(base, nested.Path)
				}
				nested.Path = filepath.Clean(nested.Path)
			}
			if nested.HostLoaded() {
				continue
			}
			command := nested.readCommand()
			child := CapturedSource{Command: command}
			if depth >= attachments.MaxDepth {
				child.Stderr, child.ExitCode = "CONTENT NOT ATTACHED: nested attachment depth limit 3 exceeded", 1
				budget.take(len(child.Stderr))
			} else {
				child = readSource(nested, depth+1, seen)
				if child.Command == "" {
					continue
				}
			}
			text := "\n" + child.Text()
			// Child body bytes have already consumed budget; account for framing.
			if budget.take(len(text)-len(child.Stdout)-len(child.Stderr)) != nil {
				return result
			}
			result.Stdout += text
			if child.ExitCode != 0 {
				result.ExitCode = 1
			}
		}
		return result
	}
	// Keep original selection indices and command identity for retained history.
	// Host-loaded instructions have no acquisition or result body.
	results := make([]CapturedSource, len(sources))
	var readers sync.WaitGroup
	parallel := 8
	if run != nil {
		// Native responses are bounded before materialization. Serialize them so
		// each command receives the actual remaining shared capture budget.
		parallel = 1
	}
	slots := make(chan struct{}, parallel)
	for i, source := range sources {
		if source.HostLoaded() {
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			readers.Wait()
			return Snapshot{}, errors.Join(budget.err, ctx.Err())
		}
		readers.Go(func() {
			defer func() { <-slots }()
			results[i] = readSource(source, 1, map[string]capturedFile{})
		})
	}
	readers.Wait()
	if budget.err != nil {
		return Snapshot{}, budget.err
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	resolvedSkills := make(map[string]string)
	for _, source := range sources {
		if source = resolve(source); source.Skill != "" && source.Path != "" {
			resolvedSkills[source.Skill] = source.Path
		}
	}
	return Snapshot{Selection: selection, Sources: results, NativeSkills: resolvedSkills}, nil
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
	// Yielded is used only by automatic completion attachments, never explicit reads.
	Yielded      map[string]int
	YieldContext string
}

func (r ChangeReader) read(ctx context.Context, source SourceIdentity, directory, script string) CapturedSource {
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
	query.Yielded = r.Yielded
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
