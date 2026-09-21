package router

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func newToolPluginTestRegistry(t *testing.T) (*toolRegistry, string) {
	return newToolPluginTestRegistryWithDeclaration(t, testToolPluginDeclaration)
}

func newToolPluginTestRegistryWithDeclaration(t *testing.T, declaration string) (*toolRegistry, string) {
	t.Helper()
	dataDirectory := t.TempDir()
	pluginDirectory := filepath.Join(dataDirectory, "plugins")
	if err := os.Mkdir(pluginDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	liveModule := filepath.Join(pluginDirectory, "proxy.mjs")
	if err := os.WriteFile(liveModule, []byte(declaration), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := buildToolRegistryForTest(t, t.Context(), dataDirectory, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Error(err)
		}
	})
	return registry, liveModule
}

// copiedToolPluginTestRegistry gives worker-contract tests an isolated,
// authenticated copy of the real configured-plugin snapshot. Those tests
// mutate or install frontends around the snapshot; plugin discovery and
// snapshot creation themselves are covered by the startup and pinning tests.
func copiedToolPluginTestRegistry(t *testing.T) *toolRegistry {
	t.Helper()
	source := pluginProxyTestFixture.get(t, testToolPluginDeclaration)
	snapshot := filepath.Join(t.TempDir(), filepath.Base(source.SnapshotDir))
	if err := os.CopyFS(filepath.Join(snapshot, "runtime"), os.DirFS(source.RuntimeRoot)); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(source.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshot, toolPluginManifestFilename), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	// Only runtime files and the manifest are mutated. Wrappers borrow the
	// parent's pinned executable instead of copying an unused test binary.
	names := []string{"shell"}
	for name := range source.wrappers {
		names = append(names, name)
	}
	for _, name := range names {
		if _, err := ensureWorkerSymlinkInDirectory(filepath.Join(source.SnapshotDir, toolWorkerExecutableFilename), snapshot, name); err != nil {
			t.Fatal(err)
		}
	}

	registry := &toolRegistry{
		SnapshotDir:       snapshot,
		RuntimeRoot:       filepath.Join(snapshot, "runtime"),
		NodeExecutable:    source.NodeExecutable,
		DiagnoseHooks:     source.DiagnoseHooks,
		builtinTranslator: source.builtinTranslator,
		frontendDirectory: filepath.Join(snapshot, "bin"),
		runtimeDirectory:  source.runtimeDirectory,
		shellRuntime:      filepath.Join(snapshot, filepath.Base(source.shellRuntime)),
		ordered:           source.ordered,
		byName:            source.byName,
		wrappers:          make(map[string]string, len(source.wrappers)),
		frontends:         make(map[string]string),
	}
	for name := range source.wrappers {
		registry.wrappers[name] = filepath.Join(snapshot, name)
	}
	return registry
}

func TestToolPluginWorkerRunsPinnedImplementationInCodexContext(t *testing.T) {
	registry, liveModule := newToolPluginTestRegistry(t)
	wrapper, ok := registry.wrapper("plugin_tool")
	if !ok {
		t.Fatal("plugin wrapper is unavailable")
	}
	if err := os.WriteFile(liveModule, []byte("throw new Error('live module changed');\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	t.Chdir(cwd)
	cwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEKUGI_PLUGIN_TEST", "inherited")

	var stdout, stderr bytes.Buffer
	handled, exitCode := RunToolPluginWorker(
		t.Context(),
		wrapper,
		[]string{"one", "two words"},
		os.Stdin,
		&stdout,
		&stderr,
	)
	if !handled || exitCode != 7 {
		t.Fatalf("worker handled %t, exit code %d, stderr %q", handled, exitCode, stderr.String())
	}
	wantStdout := strings.Join([]string{cwd, "inherited", "one", "two words"}, "|")
	if stdout.String() != wantStdout || stderr.String() != "fixture stderr" {
		t.Fatalf("worker stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}

func TestToolPluginWorkerResolvesBasenameFromPath(t *testing.T) {
	registry := copiedToolPluginTestRegistry(t)
	if err := registry.installFrontends(); err != nil {
		t.Fatal(err)
	}
	second := copiedToolPluginTestRegistry(t)
	if err := second.installFrontends(); err != nil {
		t.Fatalf("concurrent session frontend installation: %v", err)
	}
	if registry.frontendDirectory == second.frontendDirectory {
		t.Fatal("sessions share tool frontends")
	}
	frontend, ok := registry.frontends["plugin_tool"]
	if !ok {
		t.Fatal("plugin frontend is unavailable")
	}
	cwd := t.TempDir()
	t.Chdir(cwd)
	cwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(frontend))
	t.Setenv("MEKUGI_PLUGIN_TEST", "inherited")

	var stdout, stderr bytes.Buffer
	handled, exitCode := RunToolPluginWorker(
		t.Context(),
		filepath.Base(frontend),
		[]string{"one", "two words"},
		os.Stdin,
		&stdout,
		&stderr,
	)
	if !handled || exitCode != 7 {
		t.Fatalf("worker handled %t, exit code %d, stderr %q", handled, exitCode, stderr.String())
	}
	wantStdout := strings.Join([]string{cwd, "inherited", "one", "two words"}, "|")
	if stdout.String() != wantStdout || stderr.String() != "fixture stderr" {
		t.Fatalf("worker stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}

func TestToolPluginFrontendCleanupIsSessionOwned(t *testing.T) {
	first, _ := newToolPluginTestRegistry(t)
	second, _ := newToolPluginTestRegistry(t)
	if err := first.installFrontends(); err != nil {
		t.Fatal(err)
	}
	if err := second.installFrontends(); err != nil {
		t.Fatal(err)
	}
	firstFrontend := first.frontends["plugin_tool"]
	secondFrontend := second.frontends["plugin_tool"]
	if firstFrontend == secondFrontend {
		t.Fatal("sessions share a plugin frontend")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(firstFrontend); !os.IsNotExist(err) {
		t.Fatalf("closed session frontend remains: %v", err)
	}
	if _, err := os.Lstat(secondFrontend); err != nil {
		t.Fatalf("closing one session removed another frontend: %v", err)
	}
	var stdout, stderr bytes.Buffer
	handled, exitCode := RunToolPluginWorker(
		t.Context(), secondFrontend, []string{"still-active"}, os.Stdin, &stdout, &stderr,
	)
	if !handled || exitCode != 7 || !strings.HasSuffix(stdout.String(), "|still-active") || stderr.String() != "fixture stderr" {
		t.Fatalf("remaining session handled %t, exit %d, stdout %q, stderr %q", handled, exitCode, stdout.String(), stderr.String())
	}
}

const stdinToolPluginDeclaration = `import {readFileSync} from "node:fs";
export default {
  apiVersion: "mekugi-tool-plugin/v1",
  id: "stdin.test",
  tools: [{
    specification: {type: "custom", name: "stdin_tool", description: "stdin fixture"},
    parse(input) { return input; },
    argv(input) { return [input]; },
    translate(_input, api) { return api.exec(); },
    execute(argv, context) {
      const input = context.stdinFD === null ? "<none>" : readFileSync(context.stdinFD, "utf8");
      return {
        stdout: [process.cwd(), process.env.MEKUGI_PLUGIN_TEST, ...argv, input].join("|"),
        exitCode: 0,
      };
    }
  }]
};
`

func TestToolPluginFrontendRunsThroughHostProcess(t *testing.T) {
	registry, _ := newToolPluginTestRegistryWithDeclaration(t, stdinToolPluginDeclaration)
	if err := registry.installFrontends(); err != nil {
		t.Fatal(err)
	}
	frontend, ok := registry.frontends["stdin_tool"]
	if !ok {
		t.Fatal("stdin fixture frontend is unavailable")
	}
	workspace := t.TempDir()
	workspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(frontend))
	command := exec.CommandContext(t.Context(), filepath.Base(frontend),
		"-test.run=^TestToolPluginFrontendHostProcess$", "--", "one", "two words")
	command.Dir = workspace
	command.Env = append(os.Environ(),
		"MEKUGI_PLUGIN_TEST=inherited",
		"MEKUGI_TOOL_FRONTEND_HOST_PROCESS=1",
	)
	command.Stdin = strings.NewReader("stdin is not worker JSON\n")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute session frontend: %v\n%s", err, output)
	}
	want := strings.Join([]string{workspace, "inherited", "one", "two words", "stdin is not worker JSON\n"}, "|")
	if string(output) != want {
		t.Fatalf("frontend output %q, want %q", output, want)
	}
}

func TestToolPluginFrontendHostProcess(t *testing.T) {
	if os.Getenv("MEKUGI_TOOL_FRONTEND_HOST_PROCESS") == "" {
		return
	}
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		fmt.Fprintln(os.Stderr, "missing frontend argument separator")
		os.Exit(98)
	}
	handled, exitCode := RunToolPluginWorker(
		t.Context(), os.Args[0], os.Args[separator+1:], os.Stdin, os.Stdout, os.Stderr,
	)
	if !handled {
		fmt.Fprintln(os.Stderr, "frontend was not handled")
		os.Exit(99)
	}
	os.Exit(exitCode)
}

func TestBuiltinToolFrontendsRunGeneratedTypeScriptImplementations(t *testing.T) {
	registry, err := buildToolRegistryForTest(t, t.Context(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := registry.installFrontends(); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	goSource := "package sample\n\nfunc Alpha() {}\n"
	goPath := filepath.Join(workspace, "file.go")
	if err := os.WriteFile(goPath, []byte(goSource), 0o600); err != nil {
		t.Fatal(err)
	}
	nameOffset := strings.Index(goSource, "Alpha")
	goplsOutput := fmt.Sprintf(
		`{"span":{"uri":%q,"start":{"line":3,"column":6,"offset":%d},"end":{"line":3,"column":11,"offset":%d}},"description":"func Alpha()"}`+"\n",
		(&url.URL{Scheme: "file", Path: filepath.ToSlash(goPath)}).String(),
		nameOffset,
		nameOffset+len("Alpha"),
	)
	goplsPath := filepath.Join(workspace, "gopls")
	if err := os.WriteFile(goplsPath, fmt.Appendf(nil, "#!/bin/sh\ncat <<'EOF'\n%sEOF\n", goplsOutput), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspace)
	t.Setenv("PATH", workspace+string(os.PathListSeparator)+os.Getenv("PATH"))
	alphaHash := sha256.Sum256([]byte("func Alpha() {}"))

	for _, test := range []struct {
		name       string
		arguments  []string
		wantOutput string
	}{
		{name: "hcat", arguments: []string{"file.txt", "0:1"}, wantOutput: "1:8ed3 alpha\n"},
		{name: "hgrep", arguments: []string{"-F", "alpha", "file.txt"}, wantOutput: "\"file.txt\":1:8ed3 alpha\n"},
		{name: "hsymbol", arguments: []string{"def", "file.go", fmt.Sprintf("3:%x", alphaHash[:2]), "Alpha"}, wantOutput: strconv.Quote("file.go") + ":3:" + fmt.Sprintf("%x", alphaHash[:2]) + " func Alpha() {}\n"},
		{name: "inspect_file", arguments: []string{"file.txt"}, wantOutput: "{\"ok\":true,\"data\":{\"path\":\"file.txt\",\"kind\":\"none\",\"language\":null,\"size_bytes\":11,\"line_count\":null,\"parse_complete\":true,\"outline\":[]},\"truncated\":false,\"truncation\":null}\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			frontend, ok := registry.frontends[test.name]
			if !ok {
				t.Fatalf("%s session frontend is unavailable", test.name)
			}
			var stdout, stderr bytes.Buffer
			handled, exitCode := RunToolPluginWorker(
				t.Context(), frontend, test.arguments, os.Stdin, &stdout, &stderr,
			)
			if !handled || exitCode != 0 || stdout.String() != test.wantOutput || stderr.String() != "" {
				t.Fatalf(
					"%s handled %t, exit %d, stdout %q, stderr %q",
					test.name,
					handled,
					exitCode,
					stdout.String(),
					stderr.String(),
				)
			}
		})
	}
}

func TestHGrepWorkerReferencesSelectRepeatedMixedNewlineRows(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	workspace := t.TempDir()
	const baseline = "prefix\rskip\nsame\nsame\n"
	if err := os.WriteFile(filepath.Join(workspace, "mixed.txt"), []byte(baseline), 0o600); err != nil {
		t.Fatal(err)
	}
	invocation := newShellWorkerTestInvocation(workspace)
	stdout, stderr, exitCode := runShellWorkerTest(
		t,
		registry,
		"bash",
		nil,
		workerCommand("hgrep", []string{"-F", "same", "mixed.txt"}),
		os.Stdin,
		invocation,
	)
	if exitCode != 0 || stderr != "" {
		t.Fatalf("worker exit %d, stdout %q, stderr %q", exitCode, stdout, stderr)
	}
	rows := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(rows) != 2 {
		t.Fatalf("expected two matches, got %q", stdout)
	}
	for index, want := range []string{
		"prefix\rskip\nselected\nsame\n",
		"prefix\rskip\nsame\nselected\n",
	} {
		row, ok := strings.CutPrefix(rows[index], `"mixed.txt":`)
		if !ok {
			t.Fatalf("missing source path in %q", rows[index])
		}
		reference, _, ok := strings.Cut(row, " ")
		if !ok {
			t.Fatalf("missing row identity in %q", row)
		}
		got, err := mekugi.EditText(t.Context(), baseline, "type "+reference+` "selected"`)
		if err != nil {
			t.Fatalf("apply emitted reference %q: %v", reference, err)
		}
		if got != want {
			t.Fatalf("reference %q selected the wrong repeated occurrence: got %q, want %q", reference, got, want)
		}
	}
}

func TestToolPluginWorkerRejectsSnapshotMismatch(t *testing.T) {
	registry := copiedToolPluginTestRegistry(t)
	wrapper, ok := registry.wrapper("plugin_tool")
	if !ok {
		t.Fatal("plugin wrapper is unavailable")
	}
	hostPath := filepath.Join(registry.RuntimeRoot, "host.mjs")
	if err := os.WriteFile(hostPath, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	manifest.RegistryID, err = toolRegistryIdentity(manifest, registry.RuntimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeToolWorkerManifest(registry.SnapshotDir, manifest); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	handled, exitCode := RunToolPluginWorker(t.Context(), wrapper, nil, os.Stdin, &bytes.Buffer{}, &stderr)
	if !handled || exitCode != 1 || !strings.Contains(stderr.String(), "registry identity mismatch") {
		t.Fatalf("worker handled %t, exit code %d, stderr %q", handled, exitCode, stderr.String())
	}
}
func TestToolPluginWorkerRejectsMissingManifest(t *testing.T) {
	registry := copiedToolPluginTestRegistry(t)
	wrapper, ok := registry.wrapper("plugin_tool")
	if !ok {
		t.Fatal("plugin wrapper is unavailable")
	}
	if err := os.Remove(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename)); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	handled, exitCode := RunToolPluginWorker(t.Context(), wrapper, nil, os.Stdin, &bytes.Buffer{}, &stderr)
	if !handled || exitCode != 1 || !strings.Contains(stderr.String(), "open tool worker manifest") {
		t.Fatalf("worker handled %t, exit code %d, stderr %q", handled, exitCode, stderr.String())
	}
}

func TestOrdinaryBinSymlinkIsNotAPluginWorker(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "mekugi")
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	handled, code := RunToolPluginWorker(t.Context(), link, []string{"--help"}, os.Stdin, &output, &output)
	if handled || code != 0 || output.Len() != 0 {
		t.Fatalf("ordinary launcher symlink claimed as a worker: %t %d %q", handled, code, output.String())
	}
}
