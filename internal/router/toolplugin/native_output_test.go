package toolplugin

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func nativeFormatFixture(t *testing.T, args ...string) ExecutionOutput {
	t.Helper()
	output, err := FormatOutput(t.Context(), args)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func nativeReadFixture(t *testing.T, request nativeReadRequest, budget int) nativeReadPage {
	t.Helper()
	data, err := json.Marshal(&request)
	if err != nil {
		t.Fatal(err)
	}
	output := nativeFormatFixture(t, strconv.Itoa(budget), "read", string(data), "")
	if output.ExitCode != 0 || output.Stderr != "" {
		t.Fatalf("read output: %+v", output)
	}
	var page nativeReadPage
	if err := json.Unmarshal([]byte(output.Stdout), &page); err != nil {
		t.Fatal(err)
	}
	if count := nativeTokens(t, page.Text); count > budget {
		t.Fatalf("page has %d tokens, limit %d", count, budget)
	}
	return page
}

// Source: internal/router/toolplugin/tests/read-output.test.ts
func TestNativeOutputReadPagination(t *testing.T) {
	var rows strings.Builder
	for i := range 30 {
		rows.WriteString(strconv.Itoa(i+1) + ":abcd word π🙂\n")
	}
	for _, tc := range []struct {
		kind, source string
		budget       int
	}{
		{"rows", rows.String(), 80}, {"", "α🙂text" + strings.Repeat("α🙂text", 100), 32},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			request := nativeReadRequest{Stdout: tc.source, StdoutKind: tc.kind, Stream: "stdout"}
			var joined strings.Builder
			complete := false
			for range 100 {
				page := nativeReadFixture(t, request, tc.budget)
				if page.Position == request.Position && !page.Complete {
					t.Fatalf("no progress: %+v", page)
				}
				if !utf8.ValidString(page.Text) {
					t.Fatal("page splits UTF-8")
				}
				if tc.kind == "rows" && !strings.HasSuffix(page.Text, "\n") {
					t.Fatal("page splits row")
				}
				joined.WriteString(page.Text)
				request.Position = page.Position
				if page.Complete {
					complete = true
					break
				}
			}
			if !complete || joined.String() != tc.source {
				t.Fatal("pagination lost or duplicated source")
			}
		})
	}
	oversized := "1:abcd " + strings.Repeat("word ", 200) + "\n"
	request := nativeReadRequest{Stdout: oversized, StdoutKind: "rows", Stream: "stdout"}
	page := nativeReadFixture(t, request, 80)
	if page.Text != "" || page.Position != [2]int{} || page.Complete || page.NeededTokens != nativeTokens(t, oversized) {
		t.Fatalf("oversized row must not advance: %+v", page)
	}
	request.Label, request.SourceRow = "file.go", 7
	page = nativeReadFixture(t, request, 80)
	framed := "--- file.go ---\n[rows 7:7]\n" + oversized
	if page.Text != "" || page.Position != [2]int{} || page.Complete || page.NeededTokens != nativeTokens(t, framed) {
		t.Fatalf("oversized framed row must count admission framing: %+v", page)
	}
	if page := nativeReadFixture(t, nativeReadRequest{Stdout: "x", Stream: "stdout"}, 1); page.Text != "x" || !page.Complete {
		t.Fatalf("tiny page: %+v", page)
	}
}

func TestNativeOutputReadExactJSON(t *testing.T) {
	source := `[9007199254740993,1e400,{"n":9007199254740993,"s":"a,]b","a":[1e400]}]`
	page := nativeReadFixture(t, nativeReadRequest{Stdout: source, StdoutKind: "json", Stream: "stdout"}, 200)
	if page.Text != source || !page.Complete {
		t.Fatalf("raw numeric JSON changed: %+v", page)
	}
	for position, want := range []string{
		source,
		`[1e400,{"n":9007199254740993,"s":"a,]b","a":[1e400]}]`,
		`[{"n":9007199254740993,"s":"a,]b","a":[1e400]}]`,
	} {
		page := nativeReadFixture(t, nativeReadRequest{Stdout: source, StdoutKind: "json", Stream: "stdout", Position: [2]int{position, 0}}, 200)
		if page.Text != want || !page.Complete || page.Position != [2]int{3, 0} {
			t.Fatalf("raw numeric continuation %d changed: %+v", position, page)
		}
	}
	var entries []jsontext.Value
	for i := range 25 {
		entries = append(entries, jsontext.Value(`{"name":"item `+strconv.Itoa(i)+`","line":"`+strconv.Itoa(i+1)+`:abcd"}`))
	}
	encoded, err := json.Marshal(&entries)
	if err != nil {
		t.Fatal(err)
	}
	request := nativeReadRequest{Stdout: string(encoded), StdoutKind: "json", Stream: "stdout"}
	var joined []jsontext.Value
	complete := false
	for range 100 {
		page := nativeReadFixture(t, request, 96)
		var values []jsontext.Value
		if err := json.Unmarshal([]byte(page.Text), &values); err != nil {
			t.Fatalf("invalid page array: %v", err)
		}
		joined = append(joined, values...)
		request.Position = page.Position
		if page.Complete {
			complete = true
			break
		}
	}
	if !complete || !reflect.DeepEqual(joined, entries) {
		t.Fatal("JSON entries lost or changed")
	}
	request = nativeReadRequest{Stdout: source, StdoutKind: "json", Stream: "stdout"}
	page = nativeReadFixture(t, request, 1)
	if page.Text != "" || page.Position != [2]int{} || page.Complete || page.NeededTokens != nativeTokens(t, "[9007199254740993]") {
		t.Fatalf("oversized JSON entry: %+v", page)
	}
}

func TestNativeOutputReadFramesAndSelection(t *testing.T) {
	for _, tc := range []struct{ stdout, stderr, stream, want string }{
		{"", "", "", ""}, {"out", "", "", "out"}, {"out\n", "", "", "out\n"},
		{"", "err", "", "[stderr bytes]\nerr\n[/stderr]\n"},
		{"out", "err", "", "[stdout bytes]\nout\n[/stdout]\n[stderr bytes]\nerr\n[/stderr]\n"},
		{"out\n", "err\n", "", "[stdout bytes]\nout\n\n[/stdout]\n[stderr bytes]\nerr\n\n[/stderr]\n"},
		{"out", "err", "stdout", "out"}, {"out", "err", "stderr", "[stderr bytes]\nerr\n[/stderr]\n"},
	} {
		page := nativeReadFixture(t, nativeReadRequest{Stdout: tc.stdout, Stderr: tc.stderr, Stream: tc.stream}, 100)
		if page.Text != tc.want || !page.Complete {
			t.Fatalf("frame %q / %q / %q = %+v; want %q", tc.stdout, tc.stderr, tc.stream, page, tc.want)
		}
	}
	request := nativeReadRequest{Stdout: strings.Repeat("x", 200), Stderr: "[1]", StderrKind: "json"}
	page := nativeReadFixture(t, request, 4)
	if strings.Contains(page.Text, "stderr") || page.Position[1] != 0 {
		t.Fatalf("premature JSON placeholder: %+v", page)
	}
	request.Position = [2]int{200, 1}
	page = nativeReadFixture(t, request, 100)
	if page.Text != "" || !page.Complete {
		t.Fatalf("exhausted JSON placeholder: %+v", page)
	}
	request = nativeReadRequest{Stdout: "first\nsecond\n", StdoutKind: "rows", SourceRow: 10, Label: "file.go", Stream: "stdout"}
	page = nativeReadFixture(t, request, 100)
	if page.Text != "--- file.go ---\n[rows 10:11]\nfirst\nsecond\n" {
		t.Fatalf("source rows: %+v", page)
	}
	request.Position[0] = len("first\n")
	page = nativeReadFixture(t, request, 100)
	if page.Text != "--- file.go ---\n[rows 11:11]\nsecond\n" {
		t.Fatalf("continued source rows: %+v", page)
	}
}

func TestNativeOutputReadSharedStreamBudget(t *testing.T) {
	request := nativeReadRequest{Stdout: "short output", Stderr: strings.Repeat("diagnostic ", 30)}
	framed := regexp.MustCompile(`\[(stdout|stderr) bytes\]\n([\s\S]*?)\n\[/(?:stdout|stderr)\]\n`)
	var stdout, stderr strings.Builder
	complete := false
	for range 100 {
		page := nativeReadFixture(t, request, 24)
		if strings.HasPrefix(page.Text, "[") {
			for _, match := range framed.FindAllStringSubmatch(page.Text, -1) {
				if match[1] == "stdout" {
					stdout.WriteString(match[2])
				} else {
					stderr.WriteString(match[2])
				}
			}
		} else {
			stdout.WriteString(page.Text)
		}
		if page.Position == request.Position && !page.Complete {
			t.Fatalf("no stream progress: %+v", page)
		}
		request.Position = page.Position
		if page.Complete {
			complete = true
			break
		}
	}
	if !complete || stdout.String() != request.Stdout || stderr.String() != request.Stderr {
		t.Fatal("framed pagination lost stream data")
	}
}

func TestNativeOutputRejectsInvalidReadPositions(t *testing.T) {
	for _, request := range []nativeReadRequest{
		{Stdout: "α", Position: [2]int{1, 0}, Stream: "stdout"},
		{Stdout: "row\n", StdoutKind: "rows", Position: [2]int{1, 0}, Stream: "stdout"},
		{Stdout: "x", Position: [2]int{-1, 0}}, {Stdout: "x", Position: [2]int{2, 0}},
		{Stdout: "[1]", StdoutKind: "json", Position: [2]int{2, 0}},
	} {
		data, err := json.Marshal(&request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := FormatOutput(t.Context(), []string{"64", "read", string(data), ""}); err == nil {
			t.Fatalf("accepted invalid position: %+v", request)
		}
	}
}

// Source: internal/router/toolplugin/tests/tokens.test.ts
func TestNativeOutputHeadTailUnicodeAndSharedBudget(t *testing.T) {
	for _, mode := range []string{"head", "tail"} {
		for _, budget := range []int{1, 2, 7, 42, 100} {
			for _, source := range []string{"", "short\n", strings.Repeat("first 🙂 café 中文 👨‍👩‍👧‍👦\n", 30), strings.Repeat(" ", 5000), "<|endoftext|>\r\n", "\ufeffhello world more words", "before\ufeffhello world", "\ufeff\ufeff"} {
				output := nativeFormatFixture(t, strconv.Itoa(budget), mode, source, "")
				if !utf8.ValidString(output.Stdout) || nativeTokens(t, output.Stdout) > budget {
					t.Fatalf("invalid token cut: %+v", output)
				}
				if mode == "head" && !strings.HasPrefix(source, output.Stdout) || mode == "tail" && !strings.HasSuffix(source, output.Stdout) {
					t.Fatalf("selection is not %s: %q", mode, output.Stdout)
				}
			}
		}
	}
	tiny := nativeFormatFixture(t, "1", "tail", "stdout", "first error")
	if tiny.Stdout == "" || tiny.Stderr != "" {
		t.Fatalf("one-token allocation: %+v", tiny)
	}
	for _, budget := range []int{2, 7, 42} {
		output := nativeFormatFixture(t, strconv.Itoa(budget), "head", strings.Repeat("out ", 200), strings.Repeat("err ", 200))
		out, err := nativeTokens(t, output.Stdout), nativeTokens(t, output.Stderr)
		if out == 0 || err > budget/2 || out+err > budget {
			t.Fatalf("shared budget %d: %+v (%d/%d)", budget, output, out, err)
		}
	}
	for _, args := range [][]string{nil, {"0", "head", "", ""}, {"15501", "head", "", ""}, {"01", "head", "", ""}, {"1", "unknown", "", ""}} {
		if _, err := FormatOutput(t.Context(), args); err == nil || !strings.Contains(err.Error(), "invalid mrun output selection") {
			t.Fatalf("invalid arguments %q: %v", args, err)
		}
	}
}

func TestNativeOutputShellDoubleJSONAndRows(t *testing.T) {
	source := strings.Repeat("\"path\\\\name\":1:abcd \"\t🙂 café 中文\"\r\n", 500)
	for _, budget := range []int{1, 20, 123, 256, 1600, 8976} {
		output := nativeFormatFixture(t, strconv.Itoa(budget), "shell", source, "")
		var selected selectedNativeText
		if err := json.Unmarshal([]byte(output.Stdout), &selected); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(source, selected.Text) || selected.Text != "" && !strings.HasSuffix(selected.Text, "\n") {
			t.Fatalf("shell splits row: %+v", selected)
		}
		framed := 0
		if selected.Text != "" {
			once, err := json.Marshal(selected.Text)
			if err != nil {
				t.Fatal(err)
			}
			twice, err := json.Marshal(string(once))
			if err != nil {
				t.Fatal(err)
			}
			framed = nativeTokens(t, string(twice))
		}
		if selected.Tokens != framed || framed > budget {
			t.Fatalf("nested JSON budget %d: %+v, actual %d", budget, selected, framed)
		}
		rows := nativeFormatFixture(t, strconv.Itoa(budget), "rows", source, "")
		if !strings.HasPrefix(source, rows.Stdout) || rows.Stdout != "" && !strings.HasSuffix(rows.Stdout, "\n") || nativeTokens(t, rows.Stdout) > budget {
			t.Fatalf("row cut: %+v", rows)
		}
	}
}
