package requestexecution

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"strings"
	"testing"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

type preparedContentObserver struct {
	Prepared
	input []byte
	ids   map[int]string
}

func (o *preparedContentObserver) ObservePreparedContent(request *codexresponses.Request, ids map[int]string) {
	o.input = append([]byte(nil), request.Fields["input"]...)
	o.ids = ids
}

func TestPreparedContentObservesExpandedAttachmentIdentity(t *testing.T) {
	frame := "Attached file \"file.txt\" (UTF-8 bytes 0:4 of 4):\nbody"
	snapshot := attachments.Snapshot{ID: "submission", Frames: []string{frame}}
	input, _ := json.Marshal([]any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": snapshot.Encode()}}}})
	observer := new(preparedContentObserver)
	a := requestAttempt{startCtx: t.Context(), prepared: observer, request: codexresponses.Request{Fields: map[string]jsontext.Value{"input": input}}}
	if err := a.prepareWire(); err != nil {
		t.Fatal(err)
	}
	if len(observer.ids) != 1 || observer.ids[0] != snapshot.ReceiptID(frame) || strings.Contains(string(observer.input), "mekugi-file-attachments") {
		t.Fatalf("missing expanded provenance: %s %v", observer.input, observer.ids)
	}
	if string(observer.input) != string(a.request.Fields["input"]) {
		t.Fatal("observation differs from prepared request")
	}
}
