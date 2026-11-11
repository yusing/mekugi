package herdr

import (
	"bytes"
	"fmt"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestRunMissingClientAndRequestBounds(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "mh-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	var out, diagnostic bytes.Buffer
	run := func(request string) int {
		out.Reset()
		diagnostic.Reset()
		return Run(t.Context(), []string{"--pid", fmt.Sprint(os.Getpid()), "--request", request}, &out, &diagnostic)
	}
	for _, request := range []string{
		`{"method":"native/header","body":{}}`,
		`{"method":"native/observe","body":{"viewer":"panel"}}`,
		`{"method":"session/page","body":{"kind":"roster","revision":1}}`,
	} {
		if code := run(request); code != 0 || strings.TrimSpace(out.String()) != `{"available":false}` {
			t.Fatalf("missing native frontend: code=%d output=%s diagnostic=%s", code, &out, &diagnostic)
		}
	}
	for _, request := range []string{
		`{"action":"state"}`,
		`{"method":"native/start","body":{}}`,
		`{"method":"native/observe","body":{"viewer":42}}`,
		`{"method":"native/observe","body":{"unknown":true}}`,
		strings.Repeat("x", 8193),
	} {
		if code := run(request); code != 2 || out.Len() != 0 {
			t.Fatal("unsupported or oversized helper request admitted")
		}
	}
}

func TestRunIncompatibleSharedServerIsActionable(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "mh-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	path, err := sessionapi.ControlSocketPath()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := sessionapi.ListenControl(path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(sessionapi.ProtocolHeader, "7")
		http.Error(w, "incompatible control protocol", http.StatusConflict)
	})}
	defer server.Close()
	go server.Serve(listener)
	var out, diagnostic bytes.Buffer
	code := Run(t.Context(), []string{"--pid", fmt.Sprint(os.Getpid()), "--request", `{"method":"native/observe","body":{"viewer":"panel"}}`}, &out, &diagnostic)
	if code != 1 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "control protocol "+sessionapi.ProtocolVersion) || !strings.Contains(diagnostic.String(), "reconnect native terminals") {
		t.Fatalf("incompatible server became absence or generic error: code=%d output=%s diagnostic=%s", code, &out, &diagnostic)
	}
}
