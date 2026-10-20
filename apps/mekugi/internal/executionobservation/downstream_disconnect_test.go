package executionobservation

import (
	"errors"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
)

func TestDownstreamDisconnectDuringAnswerStreaming(t *testing.T) {
	transform, _, _ := newSubagentCommentaryTestTransform(t, nil)
	reader := strings.NewReader(finalAnswerTestWire(finalAnswerTestEvents(t, "final_answer")))
	disconnected := inferencegateway.DownstreamDisconnectError(websocket.CloseError{Code: websocket.StatusGoingAway, Reason: "private"})
	diagnostics := &transportcapture.Stream{}
	_, err := copySSETransformed(serverErrorWriter{err: disconnected}, reader, transform, &requestexecution.ResponseHooks{StreamDiagnostics: diagnostics})
	if !errors.Is(err, codexresponses.ErrDownstreamDisconnected) {
		t.Fatalf("lost stream disconnect: %v", err)
	}
	if diagnostics.WriteTermination != "downstream_websocket_close_1001" || diagnostics.WriteWebSocketCloseCode != 1001 {
		t.Fatalf("lost stream diagnostics: %+v", diagnostics)
	}
}
