package router

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/tokenizer"
)

func TestShellRunnerMRunAcceptedMaximumDoesNotCreateContinuation(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
		`mrun --max-tokens 15000 -- sh -c 'head -c 120000 /dev/zero | tr "\000" a'`, nil,
		newShellWorkerTestInvocation(t.TempDir(), "BASH_ENV="))
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	count, err := codec.Count(stdout)
	if status != 0 || err != nil || count != 15000 || stdout != strings.Repeat("a", 120000) ||
		stderr != "" || strings.Contains(stderr, "next_call") {
		t.Fatalf("maximum-budget delivery: status=%d bytes=%d tokens=%d stderr=%q err=%v", status, len(stdout), count, stderr, err)
	}
}

func TestShellRunnerMRunImplicitCommandBoundaryKeepsArguments(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	for _, command := range []string{"mrun -n 5 go version", "mrun -n 5 -- go version"} {
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil,
				command, nil, newShellWorkerTestInvocation(t.TempDir(), "BASH_ENV="))
			if status != 0 || !strings.HasPrefix(stdout, "go version ") || stderr != "" {
				t.Fatalf("%q: status=%d stdout=%q stderr=%q", command, status, stdout, stderr)
			}
		})
	}
}

func TestShellRunnerMRunDeliveryOverflowKeepsRowsAndRecoversOnlyCommandStderr(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	const maxLines = 20_000
	invocation := newShellWorkerTestInvocation(t.TempDir(), "BASH_ENV=")
	command := fmt.Sprintf(
		`mrun -n %d -- sh -c 'seq -f "out-%%05g" 1 25000; seq -f "err-%%05g" 1 25000 >&2'`, maxLines,
	)
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, command, nil, invocation)
	const notice = "mrun: output incomplete: 20000-line limit reached\n"
	visibleStderr, receipt, foundNotice := strings.Cut(stderr, notice)
	if status != 0 || stdout == "" || !strings.HasSuffix(stdout, "\n") ||
		!strings.HasPrefix(stdout, "out-00001\n") || strings.Count(stdout, "\n") >= maxLines ||
		!foundNotice || !strings.HasPrefix(visibleStderr, "err-00001\n") || !strings.HasSuffix(visibleStderr, "\n") {
		t.Fatalf("delivery page is not a complete bounded result: status=%d stdout=%q stderr=%q", status, stdout[:min(len(stdout), 80)], stderr[:min(len(stderr), 160)])
	}
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	stdoutTokens, err := codec.Count(stdout)
	if err != nil {
		t.Fatal(err)
	}
	stderrTokens, err := codec.Count(visibleStderr)
	if err != nil || stdoutTokens+stderrTokens > 15_500 || stderrTokens > 15_500/2 {
		t.Fatalf("delivery token allocation: stdout=%d stderr=%d err=%v", stdoutTokens, stderrTokens, err)
	}
	firstErr := regexp.MustCompile(`err-([0-9]{5})\n`).FindAllStringSubmatch(visibleStderr, -1)
	if len(firstErr) == 0 {
		t.Fatalf("initial stderr page contains no complete rows: %q", visibleStderr)
	}
	visibleRows := len(firstErr)
	if visibleStderr != mrunDeliverySequence("err", visibleRows) {
		t.Fatal("initial stderr delivery is not a complete source-row prefix")
	}
	ref := mrunDeliveryReadReference(t, receipt)
	var recovered strings.Builder
	for pageIndex := range 20 {
		page, diagnostic, pageStatus := runShellWorkerTest(t, registry, "bash", nil,
			"mread "+ref+" --stderr --max-tokens 15500", nil, invocation)
		body := mrunDeliveryStderrBody(t, page)
		if body != "" && !strings.HasSuffix(body, "\n") {
			t.Fatalf("mread split a recovered stderr row: %q", body[:min(len(body), 100)])
		}
		if strings.Contains(body, "mrun: output incomplete:") {
			t.Fatalf("generated omission notice entered retained stderr: %q", body)
		}
		recovered.WriteString(body)
		if pageStatus == 0 {
			if diagnostic != "" {
				t.Fatalf("completed stderr recovery diagnostics: %q", diagnostic)
			}
			break
		}
		ref = mrunDeliveryReadReference(t, diagnostic)
		if pageIndex == 19 {
			t.Fatal("stderr recovery exceeded its page bound")
		}
	}
	if got, want := visibleStderr+recovered.String(), mrunDeliverySequence("err", maxLines); got != want {
		t.Fatalf("genuine stderr recovery changed command rows: got %d bytes, want %d", len(got), len(want))
	}
}

func TestMRunRetainedKindUsesBytePagingForOversizedOrUnterminatedRows(t *testing.T) {
	oversized, _ := mrunOversizedLFRow(t, "a")
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{name: "ordinary rows", text: "first row\nsecond row\n", want: "rows"},
		{name: "oversized LF row", text: oversized, want: ""},
		{name: "unterminated bytes", text: "unterminated data", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mrunRetainedKind(tc.text); got != tc.want {
				t.Fatalf("retained kind = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestShellRunnerMRunRecoversOversizedLFRowsFromBothStreams(t *testing.T) {
	t.Parallel()
	registry := sharedProxyTestRegistry(t)
	const generatedNotice = "mrun: output incomplete: 1-line limit reached\n"
	stdoutRow, stdoutUnits := mrunOversizedLFRow(t, "a")
	stderrRow, stderrUnits := mrunOversizedLFRow(t, "b")
	invocation := newShellWorkerTestInvocation(t.TempDir(), "BASH_ENV=")
	command := fmt.Sprintf(`mrun -n 1 -- sh -c 'yes a | head -n %d | tr "\n" " "; printf "\n"; yes b | head -n %d | tr "\n" " " >&2; printf "\n" >&2; printf "discarded stdout\n"; printf "discarded stderr\n" >&2'`, stdoutUnits, stderrUnits)
	stdout, stderr, status := runShellWorkerTest(t, registry, "bash", nil, command, nil, invocation)
	if status != 0 || stdout != "" || strings.Count(stderr, generatedNotice) != 1 {
		t.Fatalf("oversized-row delivery: status=%d stdout=%q stderr=%q", status, stdout[:min(len(stdout), 80)], stderr[:min(len(stderr), 160)])
	}
	_, receipt, found := strings.Cut(stderr, generatedNotice)
	if !found {
		t.Fatalf("missing delivery continuation after limit notice: %q", stderr)
	}
	readID := mrunDeliveryReadReference(t, receipt)

	gotStdout := mrunRecoverDeliveryStream(t, registry, invocation, readID, "stdout")
	gotStderr := mrunRecoverDeliveryStream(t, registry, invocation, readID, "stderr")
	if gotStdout != stdoutRow || gotStderr != stderrRow {
		t.Fatalf("oversized LF row recovery changed bytes: stdout %d/%d, stderr %d/%d",
			len(gotStdout), len(stdoutRow), len(gotStderr), len(stderrRow))
	}
}

func mrunOversizedLFRow(t *testing.T, word string) (string, int) {
	t.Helper()
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	for units := maxOutputTokens; ; units += 128 {
		row := strings.Repeat(word+" ", units) + "\n"
		count, err := codec.Count(row)
		if err != nil {
			t.Fatal(err)
		}
		if count > maxOutputTokens {
			return row, units
		}
	}
}

func mrunRecoverDeliveryStream(t *testing.T, registry *toolRegistry, invocation shellWorkerTestInvocation, id, stream string) string {
	t.Helper()
	var recovered strings.Builder
	for pageIndex := range 20 {
		page, diagnostic, status := runShellWorkerTest(t, registry, "bash", nil,
			"mread "+id+" --"+stream+" --max-tokens 15500", nil, invocation)
		if status != 0 && status != 1 {
			t.Fatalf("mread %s page %d status=%d stdout=%q stderr=%q", stream, pageIndex, status, page[:min(len(page), 100)], diagnostic)
		}
		body := page
		if stream == "stderr" {
			body = mrunDeliveryStderrBody(t, page)
		}
		if strings.Contains(body, "mrun: output incomplete:") || strings.Contains(body, "discarded") {
			t.Fatalf("retained %s included a generated notice or output beyond the selected line limit", stream)
		}
		recovered.WriteString(body)
		if status == 0 {
			if diagnostic != "" {
				t.Fatalf("completed mread %s returned diagnostics: %q", stream, diagnostic)
			}
			return recovered.String()
		}
		id = mrunDeliveryReadReference(t, diagnostic)
		if pageIndex == 19 {
			t.Fatalf("mread %s exceeded its page bound", stream)
		}
	}
	return ""
}

func mrunDeliveryReadReference(t *testing.T, diagnostic string) string {
	t.Helper()
	match := regexp.MustCompile(`(?m)^read: incomplete; next_call: mread ([a-z]+[0-9]*)(?: --max-tokens [0-9]+)?$`).FindStringSubmatch(strings.TrimSpace(diagnostic))
	if len(match) != 2 {
		t.Fatalf("missing single mread continuation in %q", diagnostic)
	}
	return match[1]
}

func mrunDeliveryStderrBody(t *testing.T, page string) string {
	t.Helper()
	var framed string
	for _, kind := range []string{"rows", "bytes"} {
		if body, ok := strings.CutPrefix(page, "[stderr "+kind+"]\n"); ok {
			framed = body
			break
		}
	}
	if framed == "" {
		t.Fatalf("mread stderr page is not framed: %q", page[:min(len(page), 120)])
	}
	body, ok := strings.CutSuffix(framed, "\n[/stderr]\n")
	if !ok {
		t.Fatalf("mread stderr page has no closing frame: %q", page[:min(len(page), 120)])
	}
	return body
}

func mrunDeliverySequence(prefix string, count int) string {
	var rows strings.Builder
	for index := 1; index <= count; index++ {
		fmt.Fprintf(&rows, "%s-%05d\n", prefix, index)
	}
	return rows.String()
}
