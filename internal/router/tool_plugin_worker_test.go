package router

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
)

func newToolPluginTestRegistry(t *testing.T) (*toolRegistry, string) {
	t.Helper()
	dataDirectory := t.TempDir()
	pluginDirectory := filepath.Join(dataDirectory, "plugins")
	if err := os.Mkdir(pluginDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	liveModule := filepath.Join(pluginDirectory, "proxy.mjs")
	if err := os.WriteFile(liveModule, []byte(testToolPluginDeclaration), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := buildToolRegistry(t.Context(), dataDirectory, testMekugiToolDescription, false)
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
	if err := os.CopyFS(snapshot, os.DirFS(source.SnapshotDir)); err != nil {
		t.Fatal(err)
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

func TestBuiltinToolWorkersRunGeneratedTypeScriptImplementations(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	workspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("file.txt", []byte("alpha\nbeta\n"), 0o600); err != nil {
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
			if wrapper, ok := registry.wrapper(test.name); ok {
				t.Fatalf("%s unexpectedly has wrapper %q", test.name, wrapper)
			}
			var stdout, stderr bytes.Buffer
			handled, exitCode := RunToolPluginWorker(
				t.Context(),
				registry.shellRuntime,
				[]string{"bash", workerCommand(test.name, test.arguments)},
				os.Stdin,
				&stdout,
				&stderr,
			)
			if !handled || exitCode != 0 || stdout.String() != test.wantOutput || stderr.Len() != 0 {
				t.Fatalf(
					"%s worker handled %t, exit %d, stdout %q, stderr %q",
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
	registry := sharedProxyTestRegistry(t)
	workspace := t.TempDir()
	t.Chdir(workspace)
	const baseline = "prefix\rskip\nsame\nsame\n"
	if err := os.WriteFile("mixed.txt", []byte(baseline), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	handled, exitCode := RunToolPluginWorker(
		t.Context(), registry.shellRuntime,
		[]string{"bash", workerCommand("hgrep", []string{"-F", "same", "mixed.txt"})},
		os.Stdin, &stdout, &stderr,
	)
	if !handled || exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("worker handled %t, exit %d, stdout %q, stderr %q", handled, exitCode, stdout.String(), stderr.String())
	}
	rows := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(rows) != 2 {
		t.Fatalf("expected two matches, got %q", stdout.String())
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
