package router

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestFrontendRequestPreservesInputAndSelectedHistoryScope(t *testing.T) {
	registry := sharedProxyTestRegistry(t)
	manifest, err := readToolWorkerManifest(filepath.Join(registry.SnapshotDir, toolPluginManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	service := testHistory()
	parent, thread := uniqueTestThread("request-parent"), uniqueTestThread("request-child")
	if err := service.Attach(manifest.ReplayDirectory).Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		return tx.InitializeScope(history.Scope{Thread: thread, Namespace: parent, Parent: parent}, nil)
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(codexThreadIDEnvironment, thread)
	// Neither the process cwd nor the command's scope-like arguments select history.
	t.Chdir(t.TempDir())
	args := []string{"", "two words", "--namespace=untrusted", "../relative", "line\nbreak"}
	stdin, err := os.CreateTemp(t.TempDir(), "stdin-")
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	const input = "stdin\x00bytes\n"
	if _, err := io.WriteString(stdin, input); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	commands := slices.Clone(testCommands())
	calls := 0
	wantScope := scope.Retained{Directory: manifest.ReplayDirectory, Namespace: parent, Thread: thread}
	for i := range commands {
		if commands[i].Name == "mrun" {
			commands[i].Run = func(_ context.Context, request command.Request) (command.Output, error) {
				calls++
				if !slices.Equal(request.Args, args) || request.Stdin != stdin || request.Scope != wantScope {
					t.Fatalf("request args=%q stdin matches=%t scope=%+v, want args=%q scope=%+v", request.Args, request.Stdin == stdin, request.Scope, args, wantScope)
				}
				body, err := io.ReadAll(request.Stdin)
				if err != nil || string(body) != input {
					t.Fatalf("stdin=%q err=%v", body, err)
				}
				return command.Output{Stdout: "forwarded\n", Stderr: "diagnostic\n", ExitCode: 7}, nil
			}
		}
	}
	var stdout, stderr bytes.Buffer
	handled, status := runToolPluginWorkerInHostProcess(service, testRecovery(), commands, t.Context(), registry.frontends["mrun"], args, stdin, &stdout, &stderr)
	if !handled || status != 7 || calls != 1 || stdout.String() != "forwarded\n" || stderr.String() != "diagnostic\n" {
		t.Fatalf("handled=%t status=%d calls=%d stdout=%q stderr=%q", handled, status, calls, stdout.String(), stderr.String())
	}
}
