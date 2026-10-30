package sessionapi

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/conversation/composer"
)

func TestNativeAnswerPreparationIsOneAdmission(t *testing.T) {
	for _, admit := range []bool{false, true} {
		name := "refused"
		if admit {
			name = "admitted"
		}
		t.Run(name, func(t *testing.T) {
			n, client := nativeTestServer(t)
			var paths []string
			started, release := make(chan struct{}), make(chan struct{})
			n.prepare = func(ctx context.Context, workspace string, request InputRequest) (*PreparedInput, error) {
				if workspace != "/workspace" || request.NativeDraft == nil {
					return nil, errors.New("preparation lost captured native workspace/draft")
				}
				if len(paths) == 0 {
					close(started)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				path := filepath.Join(t.TempDir(), "acquired")
				if err := os.WriteFile(path, []byte("image copy"), 0600); err != nil {
					return nil, err
				}
				paths = append(paths, path)
				draft := request.NativeDraft.Clone()
				draft.AnswerImages = []string{path}
				draft.Files = []composer.Token{{Path: "must-not-read-again"}}
				draft.Selections = []composer.Token{{Text: "already-snapshotted"}}
				draft.Attachments = []string{"prepared snapshot"}
				return NewPreparedInput(draft, []string{path}), nil
			}
			frame := nativeTestFrame("thread", 1)
			connection, attachment := openNativeTest(t, n, client, frame, "")
			event, err := connection.Next()
			if err != nil {
				t.Fatal(err)
			}
			input := NativeAnswers{Call: "call", Answers: []NativeQuestionAnswer{{Call: "call", Question: "call/0", Draft: composer.Draft{Text: "first"}}, {Call: "call", Question: "call/1", Secret: "fixture-secret"}}}
			done := make(chan error, 1)
			go func() {
				_, err := connection.Send(t.Context(), event.Target(), NativeOperation{Answers: &input})
				done <- err
			}()
			<-started
			frame.View.Version.Revision++
			if err := n.Publish(attachment.Attachment, frame); err != nil {
				t.Fatal(err)
			}
			close(release)
			var request NativeRequest
			select {
			case request = <-n.Requests():
			case err := <-done:
				t.Fatalf("publication progress prevented prepared answer admission: %v", err)
			}
			if request.Version != event.Frame.View.Version {
				t.Fatal("acquisition silently replaced the captured answer version")
			}
			if request.Prepared == nil || len(paths) != 2 || request.Prepared.Admitted() || request.Operation.Answers.Answers[1].Draft.Text != "" || request.Operation.Answers.Answers[1].Secret != "fixture-secret" {
				t.Fatal("batch admission or secret boundary was lost")
			}
			if first := request.Operation.Answers.Answers[0].Draft; len(first.Files) != 0 || len(first.Selections) != 0 || len(first.Attachments) != 1 || first.Attachments[0] != "prepared snapshot" {
				t.Fatal("final answers retained acquisition bindings or lost prepared snapshots")
			}
			owned := make(map[string]bool)
			if admit {
				if !request.Prepared.Admit(request.Context, owned) || len(owned) != 2 {
					t.Fatal("batch did not transfer every acquisition together")
				}
				request.Reply <- ""
			} else {
				request.Reply <- "The question is no longer available."
			}
			err = <-done
			if (err == nil) != admit {
				t.Fatal("unexpected final batch admission outcome", err)
			}
			request.Prepared.Cleanup()
			for _, path := range paths {
				_, err := os.Stat(path)
				if admit && err != nil || !admit && !errors.Is(err, os.ErrNotExist) {
					t.Fatal("request cleanup crossed admission ownership", err)
				}
			}
		})
	}
}

func TestNativeAnswerPreparationFailureCleansEarlierAcquisitions(t *testing.T) {
	n := &Native{}
	path := filepath.Join(t.TempDir(), "acquired")
	if err := os.WriteFile(path, []byte("image copy"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	n.prepare = func(ctx context.Context, _ string, request InputRequest) (*PreparedInput, error) {
		calls++
		if calls == 2 {
			return nil, context.Canceled
		}
		return NewPreparedInput(request.NativeDraft.Clone(), []string{path}), nil
	}
	answers := NativeAnswers{Answers: make([]NativeQuestionAnswer, 2)}
	if _, err := n.prepareAnswers(t.Context(), "/workspace", &answers); !errors.Is(err, context.Canceled) {
		t.Fatal("later acquisition failure was lost", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed batch retained an earlier unadmitted acquisition", err)
	}
}

func TestPublicAnswerLimitsAndBatchCleanup(t *testing.T) {
	encoded := strings.Repeat("A", base64.StdEncoding.EncodedLen(UploadLimit/2))
	encoded = encoded[:len(encoded)-2] + "==" // Exactly half the decoded upload budget.
	answer := Answer{Answers: []QuestionAnswer{{Attachments: []Attachment{{Data: &encoded}}}, {Attachments: []Attachment{{Data: &encoded}}}}}
	if err := answer.validate(); err != nil {
		t.Fatal("combined limit rejected", err)
	}
	answer.Answers[1].Attachments = append(answer.Answers[1].Attachments, Attachment{Data: new("YQ==")})
	if err := answer.validate(); err == nil {
		t.Fatal("combined upload excess accepted")
	}
	if err := (Answer{Answers: []QuestionAnswer{{Text: strings.Repeat("x", inputLimit+1)}}}).validate(); err == nil {
		t.Fatal("answer metadata excess accepted")
	}
	path := filepath.Join(t.TempDir(), "acquired")
	if err := os.WriteFile(path, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err := answer.prepare(t.Context(), "/workspace", func(context.Context, string, InputRequest) (*PreparedInput, error) {
		calls++
		if calls == 2 {
			return nil, context.Canceled
		}
		return NewPreparedInput(composer.Draft{}, []string{path}), nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("batch acquisition failure lost", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed batch leaked earlier image", err)
	}
}
