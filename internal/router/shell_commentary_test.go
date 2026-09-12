package router

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

type recordingCommentarySink struct {
	mu        sync.Mutex
	texts     []string
	completed bool
}

func (s *recordingCommentarySink) Publish(_ context.Context, text string) error {
	s.mu.Lock()
	s.texts = append(s.texts, text)
	s.mu.Unlock()
	return nil
}

func (s *recordingCommentarySink) Complete(context.Context) error {
	s.mu.Lock()
	s.completed = true
	s.mu.Unlock()
	return nil
}

func TestShellCommentaryPublishesExpandedTextWithoutChangingEvaluation(t *testing.T) {
	source := "journal() { echo hijacked; return 9; }\n" +
		"for i in 1 2; do journal add \"Running $i/2\"; false; done\n" +
		"echo continued\n"
	program, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(source), "")
	if err != nil {
		t.Fatal(err)
	}
	sink := new(recordingCommentarySink)
	var output bytes.Buffer
	runner, err := interp.New(
		interp.StdIO(nil, &output, &output),
		interp.CallHandler(shellCommentaryCallHandler(sink)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(t.Context(), program); err != nil {
		t.Fatal(err)
	}
	if output.String() != "continued\n" || len(sink.texts) != 2 ||
		sink.texts[0] != `{"op":"add","text":"Running 1/2"}` || sink.texts[1] != `{"op":"add","text":"Running 2/2"}` {
		t.Fatalf("output = %q, commentary = %q", output.String(), sink.texts)
	}
}

func TestShellJournalRejectsUnavailablePublisher(t *testing.T) {
	program, err := syntax.NewParser().Parse(strings.NewReader("journal add hidden\necho continued\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	runner, err := interp.New(
		interp.StdIO(nil, &output, &output),
		interp.CallHandler(shellCommentaryCallHandler(nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(t.Context(), program); err == nil || output.Len() != 0 {
		t.Fatalf("output = %q, error %v", output.String(), err)
	}
}
