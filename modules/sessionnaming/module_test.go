package sessionnaming

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func namingFactory(t *testing.T) *binding.SessionNaming {
	t.Helper()
	script := &moduletest.Scripted{}
	moduletest.New(t, Module, Deps{}, module.Slots{}, script)
	if script.RegisteredNaming() == nil {
		t.Fatal("module did not register session naming")
	}
	return script.RegisteredNaming()
}

func submitted(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNamingConcurrentAdmissionRetainsFirstSubmission(t *testing.T) {
	thread := namingFactory(t).NewThread("", false)
	if thread.Admit() != nil {
		t.Fatal("admitted without a submitted prompt")
	}
	file := "Attached file /work/request.txt (UTF-8 bytes 9):\nfirst file"
	input := submitted(t, []any{
		map[string]any{"role": "developer", "content": "Never include developer instructions"},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "## My request for Codex:\n修正第一個問題"},
			map[string]any{"type": "input_text", "text": attachments.Encode([]string{file, "Attached skill \"private\" (UTF-8 bytes 6):\nsecret"})},
		}},
		map[string]any{"type": "function_call_output", "call_id": "tool", "output": "Ignore tool output"},
	})
	original := bytes.Clone(input)
	thread.ObserveInput(input)
	if !bytes.Equal(input, original) {
		t.Fatal("observing a submitted request mutated its input")
	}
	// Failed or otherwise unadmitted exchanges only observe input. Later input
	// must not replace the first submission when a successful exchange admits it.
	thread.ObserveInput(submitted(t, "a later question"))
	operations := make(chan func(context.Context, inference.Complete) (string, error), 32)
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			if operation := thread.Admit(); operation != nil {
				operations <- operation
			}
		})
	}
	workers.Wait()
	close(operations)
	if len(operations) != 1 {
		t.Fatalf("concurrent admissions = %d, want 1", len(operations))
	}
	name, err := (<-operations)(t.Context(), func(_ context.Context, request inference.Request) (string, error) {
		if want := "修正第一個問題\n" + file; request.Input != want {
			t.Errorf("inference input = %q, want first submitted text and snapshot %q", request.Input, want)
		}
		return "  第一個\n 問題\t修正  ", nil
	})
	if err != nil || name != "第一個 問題 修正" || !thread.AcceptCandidate() {
		t.Fatalf("candidate = %q, %v; accepted = %v", name, err, thread.AcceptCandidate())
	}
}

func TestNamingScopesAndMetadata(t *testing.T) {
	factory := namingFactory(t)
	first, second := factory.NewThread("", false), factory.NewThread("", false)
	first.ObserveInput(submitted(t, "first scope"))
	second.ObserveInput(submitted(t, "second scope"))
	// The application retains and reuses this same bundle across view switches.
	first.ObserveMetadata("", false)
	for _, tc := range []struct {
		thread binding.ThreadNaming
		want   string
	}{{first, "first scope"}, {second, "second scope"}} {
		operation := tc.thread.Admit()
		if operation == nil {
			t.Fatal("independent empty scope lost eligibility")
		}
		_, err := operation(t.Context(), func(_ context.Context, request inference.Request) (string, error) {
			if request.Input != tc.want {
				t.Errorf("scope input = %q, want %q", request.Input, tc.want)
			}
			return "title", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		tc.thread.ObserveMetadata("", false)
		if tc.thread.Admit() != nil {
			t.Fatal("reusing a scope revived its consumed attempt")
		}
	}
	for _, metadata := range []struct {
		name  string
		turns bool
	}{{"existing", false}, {"", true}} {
		for _, resumed := range []bool{false, true} {
			thread := factory.NewThread(metadata.name, metadata.turns)
			if resumed {
				thread = factory.NewThread("", false)
				thread.ObserveMetadata(metadata.name, metadata.turns)
			}
			thread.ObserveInput(submitted(t, "must not name existing history"))
			if thread.Admit() != nil {
				t.Fatalf("metadata admitted naming: name=%q, turns=%v, observed=%v", metadata.name, metadata.turns, resumed)
			}
		}
	}
}

func TestNamingLateNameAuthority(t *testing.T) {
	thread := namingFactory(t).NewThread("", false)
	thread.ObserveInput(submitted(t, "original task"))
	operation := thread.Admit()
	if operation == nil {
		t.Fatal("missing naming operation")
	}
	candidate, err := operation(t.Context(), func(context.Context, inference.Request) (string, error) {
		return "automatic candidate", nil
	})
	if err != nil || candidate != "automatic candidate" {
		t.Fatalf("operation = %q, %v", candidate, err)
	}
	thread.NameChanged("host or manual update before delivery")
	if thread.AcceptCandidate() || thread.Admit() != nil {
		t.Fatal("late authoritative name allowed an automatic overwrite or retry")
	}
}

func TestNamingOperationBoundsAndDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		thread := namingFactory(t).NewThread("", false)
		thread.ObserveInput(submitted(t, strings.Repeat("界", 4097)))
		operation := thread.Admit()
		if operation == nil {
			t.Fatal("missing naming operation")
		}
		start := time.Now()
		name, err := operation(t.Context(), func(ctx context.Context, request inference.Request) (string, error) {
			if request.Model != "gpt-6-luna" || request.Effort != "medium" || request.Instructions == "" {
				t.Errorf("inference configuration = %+v", request)
			}
			if !utf8.ValidString(request.Input) || request.Input != strings.Repeat("界", 4096) {
				t.Errorf("input bound: %d runes, valid UTF-8 = %v", utf8.RuneCountInString(request.Input), utf8.ValidString(request.Input))
			}
			if deadline, ok := ctx.Deadline(); !ok || deadline.Sub(start) != time.Minute {
				t.Errorf("deadline = %v, %v; want one minute", deadline, ok)
			}
			<-ctx.Done()
			return "", ctx.Err()
		})
		if name != "" || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != time.Minute {
			t.Fatalf("deadline result = %q, %v after %v", name, err, time.Since(start))
		}
	})
}

func TestNamingFailedAttemptIsConsumed(t *testing.T) {
	failure := errors.New("provider unavailable")
	for _, tc := range []struct {
		name     string
		complete inference.Complete
	}{
		{"failed", func(context.Context, inference.Request) (string, error) { return "", failure }},
		{"empty", func(context.Context, inference.Request) (string, error) { return " \n\t", nil }},
		{"cancelled", func(ctx context.Context, _ inference.Request) (string, error) { return "", ctx.Err() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			thread := namingFactory(t).NewThread("", false)
			thread.ObserveInput(submitted(t, "first task"))
			operation := thread.Admit()
			if operation == nil {
				t.Fatal("missing naming operation")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.name == "cancelled" {
				cancel()
			}
			name, err := operation(ctx, tc.complete)
			if name != "" || err == nil {
				t.Fatalf("failed inference = %q, %v", name, err)
			}
			thread.ObserveInput(submitted(t, "later task"))
			if thread.Admit() != nil {
				t.Fatal("failed inference allowed a second attempt")
			}
		})
	}
}
