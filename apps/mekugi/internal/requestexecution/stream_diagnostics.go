// Source: apps/mekugi/internal/router/stream_diagnostics.go:1:71@b9679c93.
package requestexecution

import (
	"errors"
	"io"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"

	"github.com/coder/websocket"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/httptransport"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
)

// Observe decoded/adapted body delivery, not wire bytes, without buffering content.
type diagnosticStreamReader struct {
	reader      io.Reader
	diagnostics *transportcapture.Stream
	receivedAt  *time.Time
}

func (r diagnosticStreamReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	if n > 0 {
		at := time.Now()
		*r.receivedAt = at
		r.diagnostics.ReadBytes(n, at)
	}
	recordStreamReadEnd(r.diagnostics, err, "")
	return n, err
}

// Request rejections pass through transport classification unchanged.
func streamTerminationCode(err error) string {
	if rejection, ok := errors.AsType[*inferencegateway.CompatibilityError](err); ok {
		return rejection.Code
	}
	if diagnostic, ok := errors.AsType[*DiagnosticError](ForwardError(err)); ok {
		return diagnostic.Code
	}
	return ""
}

func recordStreamReadEnd(d *transportcapture.Stream, err error, origin string) {
	if d == nil || err == nil {
		return
	}
	code := streamTerminationCode(err)
	if errors.Is(err, httptransport.ErrStreamIdleTimeout) {
		code = "upstream_idle_timeout"
	}
	d.ReadEnded(time.Now(), code, origin, int(websocket.CloseStatus(err)))
}

func recordStreamCopyStop(d *transportcapture.Stream, err error) {
	if d == nil {
		return
	}
	category := ""
	switch {
	case errors.Is(err, codexresponses.ErrWrite):
		category = "downstream_write_error"
		d.WriteStopped(streamTerminationCode(err), int(websocket.CloseStatus(err)))
	case errors.Is(err, codexresponses.ErrTransform):
		category = "translation_error"
	case err != nil:
		category = "read_error"
	}
	d.CopyStopped(category)
}
