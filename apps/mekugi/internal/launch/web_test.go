package launch

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRunWebArguments(t *testing.T) {
	for _, test := range []struct {
		args []string
		code int
		text string
	}{
		{[]string{"--help"}, 0, "127.0.0.1:7777"},
		{[]string{"--session", "thread"}, 2, "flag provided but not defined"},
		{[]string{"--token-file", "secret"}, 2, "flag provided but not defined"},
		{[]string{"thread"}, 2, "unexpected positional arguments"},
		{[]string{"--listen", "invalid-address"}, 1, "cannot listen"},
	} {
		var diagnostic bytes.Buffer
		if code := RunWeb(t.Context(), test.args, &diagnostic); code != test.code || !strings.Contains(diagnostic.String(), test.text) {
			t.Errorf("RunWeb(%q) = %d, %q", test.args, code, diagnostic.String())
		}
	}
}

func TestRunWebLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	done := make(chan int, 1)
	go func() { done <- RunWeb(ctx, []string{"--listen", "127.0.0.1:0"}, writer) }()
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(reader)
		scanner.Scan()
		ready <- scanner.Text()
	}()
	var address string
	select {
	case line := <-ready:
		var ok bool
		address, ok = strings.CutPrefix(line, "Web UI listening on ")
		if !ok {
			t.Fatalf("startup = %q", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("web host did not become ready")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || (response.StatusCode != http.StatusOK && (response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "make webui"))) {
		t.Fatalf("page = %d %q, %v", response.StatusCode, body, err)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("canceled host exit = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("web host did not stop")
	}
	if response, err := client.Get(address); err == nil {
		response.Body.Close()
		t.Fatal("web host kept listening after cancellation")
	}
}
