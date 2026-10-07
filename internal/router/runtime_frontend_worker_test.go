package router

import (
	json "encoding/json/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yusing/mekugi"
)

type runtimeFrontendWorkerFixture struct {
	registry *toolRegistry
	owner    *nativeObservationOwner
	store    *mekugiReplayStore
	root     ObservationBinding
	reply    atomic.Pointer[ObservationBinding]
	requests atomic.Int32
}

func newRuntimeFrontendWorkerFixture(t *testing.T) *runtimeFrontendWorkerFixture {
	t.Helper()
	owner, store, root := nativeObservationFixture(t)
	f := &runtimeFrontendWorkerFixture{owner: owner, store: store, root: root}
	f.reply.Store(&root)
	endpoint := ObservationEndpoint{Socket: filepath.Join(t.TempDir(), "context.sock"), Token: "private-test-capability"}
	listener, err := net.Listen("unix", endpoint.Socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/frontend/context" || r.Header.Get("Authorization") != "Bearer "+endpoint.Token || r.ContentLength > 0 {
			t.Errorf("unexpected frontend IPC: %s %s", r.Method, r.URL.Path)
			http.Error(w, "invalid context request", http.StatusBadRequest)
			return
		}
		if err := json.MarshalWrite(w, struct {
			Binding ObservationBinding `json:"binding"`
		}{*f.reply.Load()}); err != nil {
			t.Error(err)
		}
	})}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	f.registry, err = buildRuntimeToolRegistry(t.Context(), t.TempDir(), t.TempDir(), store.directory,
		runtimeFrontendBinding{Runtime: root.Runtime, Workspace: root.Workspace, Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.registry.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := f.registry.installFrontends(); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *runtimeFrontendWorkerFixture) run(t *testing.T, directory, command string) (string, string, int) {
	t.Helper()
	return runShellWorkerTest(t, f.registry, "bash", nil, command, nil,
		newShellWorkerTestInvocation(directory, "CODEX_THREAD_ID=inherited-codex-thread", routerTestWorkerUnscopedEnvironment+"=0"))
}

func (f *runtimeFrontendWorkerFixture) edit(t *testing.T, binding ObservationBinding, id, before, after string) string {
	t.Helper()
	path := filepath.Join(f.root.Workspace, "a.txt")
	nativeObservationWrite(t, path, before)
	call := ObservationCall{Binding: binding, ID: id, Tool: "Edit", Input: id, Paths: []string{path}}
	if err := f.owner.before(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	nativeObservationWrite(t, path, after)
	change, err := f.owner.after(t.Context(), call, ObservationTerminal{Status: "completed"})
	if err != nil || change == "" {
		t.Fatalf("native edit: %q %v", change, err)
	}
	return change
}

func TestRuntimeFrontendRegistryAndIdentity(t *testing.T) {
	t.Parallel()
	f := newRuntimeFrontendWorkerFixture(t)
	var names []string
	for name := range f.registry.frontends {
		names = append(names, name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"inspect_file", "mcat", "mchanges", "mread", "mrun", "msymbol"}) {
		t.Fatalf("runtime frontends = %v", names)
	}
	manifest, err := readToolWorkerManifest(filepath.Join(f.registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil || manifest.Runtime == nil {
		t.Fatalf("runtime manifest: %#v %v", manifest.Runtime, err)
	}
	for _, field := range []string{"runtime", "workspace", "socket", "token"} {
		t.Run(field, func(t *testing.T) {
			changed := manifest
			binding := *manifest.Runtime
			changed.Runtime = &binding
			switch field {
			case "runtime":
				binding.Runtime += "-different"
			case "workspace":
				binding.Workspace += "/different"
			case "socket":
				binding.Endpoint.Socket += "-different"
			case "token":
				binding.Endpoint.Token += "-different"
			}
			identity, err := toolRegistryIdentity(changed, f.registry.RuntimeRoot)
			if err != nil || identity == manifest.RegistryID {
				t.Fatalf("runtime %s not authenticated: %q %v", field, identity, err)
			}
		})
	}
	manifest.Runtime.Endpoint.Token = "tampered"
	if err := writeToolWorkerManifest(f.registry.SnapshotDir, manifest); err != nil {
		t.Fatal(err)
	}
	_, diagnostic, status := f.run(t, f.root.Workspace, "mcat a.txt")
	if status == 0 || !strings.Contains(diagnostic, "registry identity mismatch") || f.requests.Load() != 0 {
		t.Fatalf("tampered capability used: %q %d requests=%d", diagnostic, status, f.requests.Load())
	}
}

func TestRuntimeFrontendRegistryValidatesOmittedBuiltinCollision(t *testing.T) {
	t.Parallel()
	data := t.TempDir()
	plugins := filepath.Join(data, "plugins")
	if err := os.MkdirAll(plugins, 0700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(plugins, "collision.mjs"), strings.Replace(testToolPluginDeclaration, "plugin_tool", "mjournal", 1))
	registry, err := buildRuntimeToolRegistry(t.Context(), data, t.TempDir(), t.TempDir(), runtimeFrontendBinding{
		Runtime: "claude", Workspace: t.TempDir(), Endpoint: ObservationEndpoint{Socket: "/not-used.sock", Token: "test"},
	})
	if registry != nil {
		_ = registry.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "tool name \"mjournal\" is owned by both") {
		t.Fatalf("omission bypassed full registry validation: %v", err)
	}
}

func TestRuntimeFrontendMChangesSelectionAndWorkspace(t *testing.T) {
	t.Parallel()
	f := newRuntimeFrontendWorkerFixture(t)
	child := f.root
	child.Agent = "child"
	if err := f.owner.bind(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	id := f.edit(t, child, "child-edit", "old\n", "new\n")
	subdir := filepath.Join(f.root.Workspace, "subdir")
	if err := os.Mkdir(subdir, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "workspace-alias")
	if err := os.Symlink(f.root.Workspace, alias); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{"", " --workspace ..", " --workspace " + shellQuoteArgument(alias)} {
		out, diagnostic, status := f.run(t, subdir, "mchanges --summary "+id+args)
		if status != 0 || diagnostic != "" || out != "M\t1\t1\ta.txt\n" {
			t.Fatalf("root-pinned read %q: %q %q %d", args, out, diagnostic, status)
		}
	}
	for _, args := range []string{"", "--list", "--mine", "--mine --summary", "--net"} {
		out, diagnostic, status := f.run(t, subdir, "mchanges "+args)
		if status == 0 || out != "" || !strings.Contains(diagnostic, "caller unavailable") {
			t.Fatalf("implicit caller %q: %q %q %d", args, out, diagnostic, status)
		}
	}
	for _, view := range []string{"--summary", "apply", "revert"} {
		out, diagnostic, status := f.run(t, subdir, "mchanges "+view+" "+id+" --workspace "+shellQuoteArgument(t.TempDir()))
		if status == 0 || out != "" || !strings.Contains(diagnostic, "workspace does not match") || readTestFile(t, filepath.Join(f.root.Workspace, "a.txt")) != "new\n" {
			t.Fatalf("foreign workspace %s: %q %q %d", view, out, diagnostic, status)
		}
	}
	ctx, release, err := f.store.beginSession(t.Context(), "inherited-codex-thread", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// IDs are session-local, so deliberately advance the foreign stream past
	// the local ID rather than mistaking equal spellings for global identities.
	putTestChange(t, ctx, f.store, f.root.Workspace, "foreign-first", mekugi.RenderReviewFile("a.txt", "a.txt", "old\n", "foreign\n"))
	foreign := putTestChange(t, ctx, f.store, f.root.Workspace, "foreign-second", mekugi.RenderReviewFile("a.txt", "a.txt", "old\n", "foreign\n"))
	for _, view := range []string{"--summary", "--list", "--net", "apply", "revert"} {
		out, diagnostic, status := f.run(t, subdir, "mchanges "+view+" "+id+" "+foreign)
		if status == 0 || !strings.Contains(diagnostic, foreign) || readTestFile(t, filepath.Join(f.root.Workspace, "a.txt")) != "new\n" {
			t.Fatalf("foreign namespace %s: %q %q %d", view, out, diagnostic, status)
		}
		if strings.HasPrefix(view, "--") && !strings.Contains(out, "a.txt") && !strings.Contains(out, id) {
			t.Fatalf("foreign ID discarded valid read %s: %q", view, out)
		}
	}
}

func TestRuntimeFrontendMutationRemainsLocalAndObserved(t *testing.T) {
	t.Parallel()
	f := newRuntimeFrontendWorkerFixture(t)
	id := f.edit(t, f.root, "initial-edit", "one\ntwo\n", "one\nTWO\n")
	child := f.root
	child.Agent = "child"
	if err := f.owner.bind(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"revert", "apply"} {
		command := "mchanges " + operation + " " + id
		call := ObservationCall{Binding: child, ID: operation, Tool: "Bash", Input: command, Command: command, Shell: "/bin/bash"}
		if err := f.owner.before(t.Context(), call); err != nil {
			t.Fatal(err)
		}
		beforeRequests := f.requests.Load()
		out, diagnostic, status := f.run(t, f.root.Workspace, command)
		if status != 0 || diagnostic != "" || !strings.Contains(out, "undo:") || f.requests.Load() != beforeRequests+1 {
			t.Fatalf("local %s: %q %q %d", operation, out, diagnostic, status)
		}
		newID, err := f.owner.after(t.Context(), call, ObservationTerminal{Status: "completed", Report: out})
		if err != nil || newID == "" || newID == id {
			t.Fatalf("observed %s not a new capture: %q %v", operation, newID, err)
		}
		history := nativeObservationHistory(t, f.store, call, "after")
		if history.ExecutingThread != observationThread(child) || len(history.ReviewFiles) != 1 || history.ChangeID != newID {
			t.Fatalf("root retention stole child authorship: %#v", history)
		}
		want := "one\nTWO\n"
		if operation == "revert" {
			want = "one\ntwo\n"
		}
		if got := readTestFile(t, filepath.Join(f.root.Workspace, "a.txt")); got != want {
			t.Fatalf("%s content = %q", operation, got)
		}
	}
	invalid := f.root
	invalid.Session = ""
	f.reply.Store(&invalid)
	_, diagnostic, status := f.run(t, f.root.Workspace, "mchanges revert "+id)
	if status == 0 || !strings.Contains(diagnostic, "root session binding") || readTestFile(t, filepath.Join(f.root.Workspace, "a.txt")) != "one\nTWO\n" {
		t.Fatalf("mutation executed without context: %q %d", diagnostic, status)
	}
}

func TestRuntimeFrontendOutputRecoveryUsesRootSession(t *testing.T) {
	t.Parallel()
	f := newRuntimeFrontendWorkerFixture(t)
	text := strings.Repeat("retained runtime output\n", 60)
	writeTestFile(t, filepath.Join(f.root.Workspace, "output.txt"), text)
	out, diagnostic, status := f.run(t, f.root.Workspace, "mcat output.txt --max-tokens 32")
	_, next, found := strings.Cut(diagnostic, "next_call: mread ")
	if status == 0 || !found || len(strings.Fields(next)) == 0 {
		t.Fatalf("output not retained: %q %q %d", out, diagnostic, status)
	}
	handle := strings.Fields(next)[0]
	remainder, diagnostic, status := f.run(t, f.root.Workspace, "mread "+handle+" --max-tokens 15500")
	rowLabel, remainingRows, hasRows := strings.Cut(remainder, "\n")
	if status != 0 || diagnostic != "" || !hasRows || !strings.HasPrefix(rowLabel, "[rows ") || out+remainingRows != text {
		t.Fatalf("fresh worker recovery: %q %q %d", remainder, diagnostic, status)
	}
	other := f.root
	other.Session = "other-session"
	f.reply.Store(&other)
	remainder, diagnostic, status = f.run(t, f.root.Workspace, "mread "+handle)
	if status == 0 || remainder != "" || diagnostic == "" {
		t.Fatalf("retained output crossed sessions: %q %q %d", remainder, diagnostic, status)
	}
	if _, err := os.Stat(filepath.Join(f.store.directory, storageSessionName("inherited-codex-thread"))); !os.IsNotExist(err) {
		t.Fatalf("inherited Codex identity was retained: %v", err)
	}
}

func TestRuntimeFrontendMutationConflictAndPendingDependency(t *testing.T) {
	t.Parallel()
	f := newRuntimeFrontendWorkerFixture(t)
	id := f.edit(t, f.root, "initial-edit", "one\ntwo\n", "one\nTWO\n")
	ctx, release, err := f.store.beginSession(t.Context(), observationThread(f.root), "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	pending, err := f.store.reserveChange(ctx, f.root.Workspace, observationThread(f.root), "pending")
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"apply", "revert"} {
		out, diagnostic, status := f.run(t, f.root.Workspace, "mchanges "+operation+" "+id+" "+pending)
		if status == 0 || out != "" || !strings.Contains(diagnostic, "pending") || readTestFile(t, filepath.Join(f.root.Workspace, "a.txt")) != "one\nTWO\n" {
			t.Fatalf("%s skipped a dependency: %q %q %d", operation, out, diagnostic, status)
		}
	}
	nativeObservationWrite(t, filepath.Join(f.root.Workspace, "a.txt"), "one\nhand edited\n")
	command := "mchanges revert " + id
	call := ObservationCall{Binding: f.root, ID: "conflicting-revert", Tool: "Bash", Input: command, Command: command, Shell: "/bin/bash"}
	if err := f.owner.before(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	out, diagnostic, status := f.run(t, f.root.Workspace, command)
	if status != 1 || diagnostic != "" || !strings.Contains(out, "UU") {
		t.Fatalf("conflict handling: %q %q %d", out, diagnostic, status)
	}
	content := readTestFile(t, filepath.Join(f.root.Workspace, "a.txt"))
	if !strings.Contains(content, "<<<<<<<") || !strings.Contains(content, "hand edited") {
		t.Fatalf("conflict markers not preserved: %q", content)
	}
	newID, err := f.owner.after(t.Context(), call, ObservationTerminal{Status: "failed", Report: out})
	if err != nil || newID == "" || newID == id {
		t.Fatalf("failed mutation effects were not captured: %q %v", newID, err)
	}
}
