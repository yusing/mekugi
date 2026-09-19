package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

func (s *recordingCommentarySink) RequestJournal(ctx context.Context, command shellJournalCommand) (shellJournalResult, error) {
	if command.Mutation == nil {
		return shellJournalResult{}, errors.New("recorder expects a single mutation")
	}
	if err := s.Publish(ctx, string(mustMarshalJSON(*command.Mutation))); err != nil {
		return shellJournalResult{}, err
	}
	return shellJournalResult{IDs: []string{fmt.Sprintf("j%d", len(s.texts))}}, nil
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
	if output.String() != "j1\nj2\ncontinued\n" || len(sink.texts) != 2 ||
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
func TestParseShellJournalCommandSupportsDedicatedOperations(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want shellJournalCommand
	}{
		{
			name: "answer add",
			argv: []string{"journal", "add", "Answer", "--answer", "--report-now"},
			want: shellJournalCommand{
				Op:       "add",
				Mutation: &journalMutation{Op: "add", Text: new("Answer"), Answer: new(true), ReportNow: true},
			},
		},
		{
			name: "agent list",
			argv: []string{"journal", "list", "/root/child"},
			want: shellJournalCommand{Op: "list", Agent: "/root/child"},
		},
		{
			name: "batch",
			argv: []string{"journal", "batch", `[{"op":"add","text":"one"},{"op":"add","text":"two"}]`},
			want: shellJournalCommand{
				Op: "batch",
				Batch: []journalMutation{
					{Op: "add", Text: new("one")},
					{Op: "add", Text: new("two")},
				},
			},
		},
		{
			name: "finish",
			argv: []string{"journal", "finish", `[{"op":"add","text":"last"}]`},
			want: shellJournalCommand{
				Op:    "finish",
				Batch: []journalMutation{{Op: "add", Text: new("last")}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseShellJournalCommand(test.argv)
			if err != nil {
				t.Fatal(err)
			}
			if got.Op != test.want.Op || got.Agent != test.want.Agent ||
				len(got.Batch) != len(test.want.Batch) {
				t.Fatalf("command = %+v, want %+v", got, test.want)
			}
			if got.Mutation == nil || test.want.Mutation == nil {
				if got.Mutation != nil || test.want.Mutation != nil {
					t.Fatalf("mutation = %+v, want %+v", got.Mutation, test.want.Mutation)
				}
			} else if got.Mutation.Op != test.want.Mutation.Op ||
				got.Mutation.ID != test.want.Mutation.ID ||
				*got.Mutation.Text != *test.want.Mutation.Text ||
				!sameJSONValue(mustMarshalJSON(got.Mutation.Answer), mustMarshalJSON(test.want.Mutation.Answer)) ||
				got.Mutation.ReportNow != test.want.Mutation.ReportNow {
				t.Fatalf("mutation = %+v, want %+v", got.Mutation, test.want.Mutation)
			}
			for index := range got.Batch {
				if got.Batch[index].Op != test.want.Batch[index].Op || *got.Batch[index].Text != *test.want.Batch[index].Text {
					t.Fatalf("batch = %+v, want %+v", got.Batch, test.want.Batch)
				}
			}
		})
	}
}

func TestShellJournalAddIDSupportsCommandSubstitution(t *testing.T) {
	program, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(
		strings.NewReader("id=$(journal add one)\nprintf 'captured=%s\\n' \"$id\"\n"), "")
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
	if output.String() != "captured=j1\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestShellJournalExactOperandsAndDeletionRetraction(t *testing.T) {
	for _, text := range []string{"--example", "--json", "--answer", "a\nb"} {
		command, err := parseShellJournalCommand([]string{"journal", "add", text})
		if err != nil || command.Mutation == nil || *command.Mutation.Text != text {
			t.Fatalf("add %q: %+v, %v", text, command, err)
		}
		command, err = parseShellJournalCommand([]string{"journal", "edit", "j1", text, "--report-now"})
		if err != nil || command.Mutation == nil || *command.Mutation.Text != text || !command.Mutation.ReportNow {
			t.Fatalf("edit %q: %+v, %v", text, command, err)
		}
	}
	command, err := parseShellJournalCommand([]string{"journal", "delete", "j1", "--report-now"})
	if err != nil || command.Mutation == nil || !command.Mutation.ReportNow {
		t.Fatalf("delete retraction: %+v, %v", command, err)
	}
	for _, argv := range [][]string{
		{"journal", "delete", "j1", "--answer"},
		{"journal", "add", "text", "--answer", "--clear-answer"},
		{"journal", "add", "text", "--json"},
		{"journal", "add", "text", "--unknown"},
		{"journal", "list", "--report-now"},
	} {
		if _, err := parseShellJournalCommand(argv); err == nil {
			t.Fatalf("accepted invalid command: %q", argv)
		}
	}
}
