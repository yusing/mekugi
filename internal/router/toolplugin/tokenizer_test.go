package toolplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tiktoken-go/tokenizer"
)

func TestGPT5TokenizerMatchesPluginFixtures(t *testing.T) {
	t.Parallel()
	codec, err := tokenizer.ForModel(tokenizer.GPT5)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("tests/testdata/gpt5_tokens.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Text   string `json:"text"`
		Tokens int    `json:"tokens"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		got, err := codec.Count(fixture.Text)
		if err != nil {
			t.Fatalf("count %q: %v", fixture.Text, err)
		}
		if got != fixture.Tokens {
			t.Errorf("count %q = %d, want %d", fixture.Text, got, fixture.Tokens)
		}
	}
}

func newFormatterFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	// Copy only the host and generated JavaScript. The formatter must not
	// instantiate the source-analysis core or load a tool declaration.
	builtins, err := fs.Sub(runtimeFiles, "dist")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(root, snapshotDirectory, "builtin"), builtins); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, snapshotDirectory, "builtin", "tools.js")); err != nil {
		t.Fatal(err)
	}
	host, err := runtimeFiles.ReadFile(hostFilename)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, hostFilename), host, 0o600); err != nil {
		t.Fatal(err)
	}
	node, err := resolveNodeRuntime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return node, root
}

func TestFormatOutputDoesNotLoadToolDeclarationsOrWASM(t *testing.T) {
	t.Parallel()
	node, root := newFormatterFixture(t)
	result, err := FormatOutput(t.Context(), node, root, []string{"3", "head", "hello world", "diagnostic"})
	if err != nil || result.Stdout != "hello" || result.Stderr != "diagnostic" || result.ExitCode != 0 {
		t.Fatalf("isolated formatter = %+v, %v", result, err)
	}
}

func TestFormatOutputBatchMatchesStandaloneFormatting(t *testing.T) {
	t.Parallel()
	node, root := newFormatterFixture(t)
	arguments := [][]string{
		{"3", "head", "hello world", "diagnostic"},
		{"3", "tail", "first\nsecond 界\nthird\n", ""},
		{"25", "shell", "first \"quoted\"\nsecond\t界\nthird\n", ""},
	}
	got, err := FormatOutputBatch(t.Context(), node, root, arguments)
	if err != nil {
		t.Fatal(err)
	}
	for i, candidate := range arguments {
		want, err := FormatOutput(t.Context(), node, root, candidate)
		if err != nil || !reflect.DeepEqual(got[i], want) {
			t.Fatalf("candidate %d = %+v, want %+v (%v)", i, got[i], want, err)
		}
	}
}

func TestFormatOutputBatchFailsWithoutPartialResults(t *testing.T) {
	t.Parallel()
	node, root := newFormatterFixture(t)
	arguments := [][]string{{"3", "head", "valid first candidate", ""}, {"0", "head", "invalid later candidate", ""}}
	if result, err := FormatOutputBatch(t.Context(), node, root, arguments); err == nil || result != nil {
		t.Fatalf("batch returned partial results: %+v, %v", result, err)
	}
}

func TestFormatOutputBatchRejectsOversizedArgumentsBeforeHost(t *testing.T) {
	t.Parallel()
	for _, arguments := range [][][]string{
		nil,
		{{}},
		{{"1", "head", "", ""}, {"1", "head", "", ""}, {"1", "head", "", ""}, {"1", "head", "", ""}},
		{{"1", "head", strings.Repeat("x", maxFormatOutputBatchBytes/2), ""},
			{"1", "head", strings.Repeat("x", maxFormatOutputBatchBytes/2), ""}},
	} {
		result, err := FormatOutputBatch(t.Context(), "must-not-run", "", arguments)
		if err == nil || result != nil || strings.Contains(err.Error(), "start plugin") {
			t.Fatalf("invalid arguments reached host: result=%v error=%v", result, err)
		}
	}
}

func formatOutputBatchHostFixture(t *testing.T, program string) (string, string) {
	t.Helper()
	node, err := resolveNodeRuntime(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, snapshotDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, hostFilename), []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	return node, root
}

func TestFormatOutputBatchRejectsMalformedResults(t *testing.T) {
	t.Parallel()
	for _, response := range []string{
		`null`, `[]`, `[null]`, `[{}]`, `[{"stdout":""}]`, `[{"exitCode":0}]`,
		`[{"stdout":"","exitCode":0},{"stdout":"","exitCode":0}]`,
		`[{"stdout":"","exitCode":0,"unexpected":true}]`,
		`[{"stdout":"","exitCode":0}] trailing`,
	} {
		t.Run(response, func(t *testing.T) {
			t.Parallel()
			literal, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			node, root := formatOutputBatchHostFixture(t, fmt.Sprintf("process.stdout.write(%s);", literal))
			if result, err := FormatOutputBatch(t.Context(), node, root, [][]string{{"1", "head", "", ""}}); err == nil || result != nil {
				t.Fatalf("accepted malformed batch response %s: %+v", response, result)
			}
		})
	}
}

func TestFormatOutputBatchCancellationStopsRunningHost(t *testing.T) {
	t.Parallel()
	ready := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ready <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	node, root := formatOutputBatchHostFixture(t,
		fmt.Sprintf("await fetch(%q); setInterval(() => {}, 1000);", server.URL))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := FormatOutputBatch(ctx, node, root, [][]string{{"1", "head", "", ""}})
		done <- err
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("host exited before readiness: %v", err)
	case <-ctx.Done():
		t.Fatal("host did not become ready")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("running host cancellation = %v", err)
	}
}
