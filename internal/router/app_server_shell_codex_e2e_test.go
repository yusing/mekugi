//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/charmbracelet/x/vt"
)

type shellPreviewProvider struct {
	calls     atomic.Int32
	sawOutput atomic.Bool
}

func (p *shellPreviewProvider) forwardExecution(_, _ context.Context, body []byte, _ http.Header, _ string) (*http.Response, error) {
	p.calls.Add(1)
	p.sawOutput.Store(bytes.Contains(body, []byte("SHELL_MODE_OK")))
	return routerFaultCodexSuccessResponse(), nil
}

func TestAppServerShellNativeCodex(t *testing.T) {
	p := new(shellPreviewProvider)
	f := newMChangesSliceFixture(t, "shell-native")
	proxy := newProxyWithSharedTestRegistry(t, f.registry)
	proxy.replayStore = f.store
	auto, stop := newAutoLiveDiff(t.Context(), f.store.directory)
	defer stop()
	proxy.autoLiveDiff, f.store.liveDiff = auto, auto.events.publish
	target := filepath.Join(t.TempDir(), "shell-edit.txt")
	startup := filepath.Join(t.TempDir(), "frontends.bash")
	writeTestFile(t, startup, "export PATH="+quoteShellWord(f.registry.frontendDirectory)+":\"$PATH\"\n")
	before := func(t *testing.T, outer io.Writer, await func(string), awaitFrame func(func(string) bool), _ *vt.Emulator) {
		if _, err := io.WriteString(outer, "!printf 'SHELL_MODE_%s' OK; printf 'shell-body\\n' > "+quoteShellWord(target)); err != nil {
			t.Fatal(err)
		}
		await("Shell Mode")
		if _, err := io.WriteString(outer, "\r"); err != nil {
			t.Fatal(err)
		}
		await("completed")
		io.WriteString(outer, "\x022")
		awaitFrame(func(frame string) bool {
			return strings.Contains(frame, "shell-edit.txt") && strings.Contains(frame, "+1")
		})
		auto.mu.Lock()
		workspace := auto.workspace
		thread := ""
		for id := range auto.scope.Workspaces[workspace] {
			thread = id
		}
		auto.mu.Unlock()
		ctx, release, err := f.store.beginSession(t.Context(), thread, "")
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		index, err := f.store.scoped(ctx).readChangeIndex(workspace)
		if err != nil || len(index.Changes) != 1 {
			t.Fatalf("shell captures: %+v, %v", index, err)
		}
		id := ""
		for change := range index.Changes {
			id = change
		}
		io.WriteString(outer, "\x021!mchanges revert "+id+"\r")
		awaitFrame(func(frame string) bool {
			return strings.Contains(frame, "0 files +0 -0") && strings.Contains(frame, "No visible changes")
		})
		io.WriteString(outer, "!mchanges apply "+id+"\r")
		awaitFrame(func(frame string) bool {
			return strings.Contains(frame, "shell-edit.txt") && strings.Contains(frame, "+1")
		})
		io.WriteString(outer, "\x022\t")
		await("Changes")
		await("mchanges apply")
		io.WriteString(outer, "\x021")
		if p.calls.Load() != 0 {
			t.Fatal("idle shell invoked the model")
		}
	}
	runAppServerPreviewWith(t, p, proxy, appServerPreview{environment: []string{routerTestWorkerEnvironment + "=1", "BASH_ENV=" + startup}, noJournal: true, beforePrompt: before})
	if !p.sawOutput.Load() {
		t.Fatal("next user input did not carry native shell output")
	}
}
