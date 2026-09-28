//go:build journal_e2e

package router

import (
	"bytes"
	"context"
	"io"
	"net/http"
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
	before := func(t *testing.T, outer io.Writer, await func(string), _ func(func(string) bool), _ *vt.Emulator) {
		if _, err := io.WriteString(outer, "!printf 'SHELL_MODE_%s' OK"); err != nil {
			t.Fatal(err)
		}
		await("Shell Mode")
		if _, err := io.WriteString(outer, "\r"); err != nil {
			t.Fatal(err)
		}
		await("completed")
		if p.calls.Load() != 0 {
			t.Fatal("idle shell invoked the model")
		}
	}
	runAppServerPreviewWithProxyAndHooks(t, p, nil, before, nil)
	if !p.sawOutput.Load() {
		t.Fatal("next user input did not carry native shell output")
	}
}
