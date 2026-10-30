package headless

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
)

func TestHeadlessPromptAdmission(t *testing.T) {
	for _, tc := range []struct{ name, prompt, want string }{
		{"empty", "", "nonempty"}, {"whitespace", " \n\t", "nonempty"},
		{"oversized", strings.Repeat("x", (16<<20)+1), "16 MiB"},
		{"literal", "  /not-a-command\n!literal\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadPrompt(t.Context(), strings.NewReader(tc.prompt))
			if tc.want == "" {
				if err != nil || got != tc.prompt {
					t.Fatalf("got=%q err=%v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	want := errors.New("stdin failed")
	if _, err := ReadPrompt(t.Context(), failedIO{want}); !errors.Is(err, want) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	cancel()
	if _, err := ReadPrompt(ctx, reader); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type failedIO struct{ err error }

func (f failedIO) Read([]byte) (int, error)  { return 0, f.err }
func (f failedIO) Write([]byte) (int, error) { return 0, f.err }

func TestHeadlessEvidenceBeforeRejection(t *testing.T) {
	for _, message := range []appserver.Message{
		{ID: jsontext.Value(`7`), Method: "item/commandExecution/requestApproval", Params: jsontext.Value(`{"threadId":"child"}`)},
		{Method: "item/completed", Params: jsontext.Value(`{"threadId":"child","item":{"type":"agentMessage","delivery":"async","questions":[{"id":"q"}]}}`)},
	} {
		var output bytes.Buffer
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		stream := NewStream(&output, cancel)
		err := stream.Observe(message)
		if err == nil || !strings.Contains(err.Error(), "continue interactively") || ctx.Err() == nil {
			t.Fatalf("err=%v", err)
		}
		var got appserver.Message
		if err := json.Unmarshal(output.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Method != message.Method || !bytes.Equal(got.ID, message.ID) || !bytes.Equal(got.Params, message.Params) {
			t.Fatalf("got=%s", &output)
		}
		size := output.Len()
		if stream.Complete("main") == nil || output.Len() != size {
			t.Fatal("completed after failure")
		}
	}
}

func TestHeadlessFamilyJSONL(t *testing.T) {
	var output bytes.Buffer
	stream := NewStream(&output, func() {})
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			params, _ := json.Marshal(map[string]int{"child": i})
			if err := stream.Observe(appserver.Message{Method: "turn/completed", Params: params}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := stream.Complete("main"); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	if len(lines) != 33 {
		t.Fatalf("records=%d", len(lines))
	}
	for _, line := range lines {
		var message appserver.Message
		if err := json.Unmarshal(line, &message); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Contains(lines[32], []byte("mekugi/headless/completed")) {
		t.Fatal("missing final event")
	}
}

func TestHeadlessOutputFailureCancelsFamily(t *testing.T) {
	want := errors.New("output failed")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream := NewStream(failedIO{want}, cancel)
	if err := stream.Observe(appserver.Message{Method: "initialized"}); !errors.Is(err, want) || ctx.Err() == nil {
		t.Fatalf("err=%v", err)
	}
	stream.Fail(errors.New("later shutdown failure"))
	if !errors.Is(stream.Err(), want) {
		t.Fatal("lost original failure")
	}
}
