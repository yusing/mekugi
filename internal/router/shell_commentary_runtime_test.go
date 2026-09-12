package router

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/internal/shellruntime"
)

func TestDiscoverShellCommentary(t *testing.T) {
	root := t.TempDir()
	t.Setenv(shellruntime.RuntimeDirectoryEnvironment, root)
	t.Setenv(shellruntime.ThreadIDEnvironment, "thread")
	worker := filepath.Join(root, "worker")
	path, _ := shellruntime.Path(root, "thread")
	if err := os.Symlink(worker, path); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("wrong authorization")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	descriptor := shellCommentaryDescriptor{Endpoint: server.URL, Token: "test-token", Worker: worker}
	valid, _ := json.Marshal(descriptor)
	for _, test := range []struct {
		name    string
		content []byte
		worker  string
		valid   bool
	}{
		{"valid", valid, worker, true},
		{"malformed", []byte("{"), worker, false},
		{"stale worker", valid, worker + "-old", false},
		{"missing token", []byte(`{"endpoint":"http://localhost","worker":"worker"}`), worker, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path+".commentary", test.content, 0o600); err != nil {
				t.Fatal(err)
			}
			sink := discoverShellCommentary(test.worker)
			if (sink != nil) != test.valid {
				t.Fatalf("sink availability = %v", sink != nil)
			}
			if sink != nil {
				if err := sink.Publish(t.Context(), `{"op":"add","text":"hello"}`); err != nil {
					t.Fatal(err)
				}
				if err := sink.Complete(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	if calls != 1 {
		t.Fatalf("publication count = %d", calls)
	}
	if err := os.Remove(path + ".commentary"); err != nil {
		t.Fatal(err)
	}
	if discoverShellCommentary(worker) != nil {
		t.Fatal("missing descriptor accepted")
	}
	external := filepath.Join(t.TempDir(), "descriptor")
	if err := os.WriteFile(external, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, path+".commentary"); err != nil {
		t.Fatal(err)
	}
	if discoverShellCommentary(worker) != nil {
		t.Fatal("escaping symlink accepted")
	}
	if err := os.Remove(path + ".commentary"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".commentary", valid, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(worker+"-new", path); err != nil {
		t.Fatal(err)
	}
	if discoverShellCommentary(worker) != nil {
		t.Fatal("swapped locator accepted")
	}
}

func TestShellCommentaryCleanupPreservesReplacement(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "replacement"}[replacement], func(t *testing.T) {
			directory := t.TempDir()
			parent, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer parent.Close()
			name := "descriptor"
			if err := parent.WriteFile(name, []byte("owned"), 0o600); err != nil {
				t.Fatal(err)
			}
			identity, err := parent.Lstat(name)
			if err != nil {
				t.Fatal(err)
			}
			session := &shellSession{parent: parent, commentary: &ownedShellCommentary{name: name, identity: identity, content: []byte("owned")}}
			if replacement {
				if err := parent.Rename(name, "original"); err != nil {
					t.Fatal(err)
				}
				if err := parent.WriteFile(name, []byte("user"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := session.closeCommentary(); err != nil {
				t.Fatal(err)
			}
			_, err = parent.Lstat(name)
			if replacement && err != nil {
				t.Fatal("replacement removed")
			}
			if !replacement && !os.IsNotExist(err) {
				t.Fatal("owned descriptor retained")
			}
		})
	}
}

func TestPrepareShellCommentaryRefreshAndCleanup(t *testing.T) {
	proxy, _ := newShellStorageTestProxy(t)
	proxy.commentary = newCommentaryBroker()
	proxy.commentaryEndpoint = "http://127.0.0.1:8080/commentary"
	directory, err := proxy.storeShellRuntime("commentary-thread")
	if err != nil {
		t.Fatal(err)
	}
	proxy.prepareShellCommentary("commentary-thread", "history", "")
	session := proxy.shellSessions[directory]
	if session.commentary == nil {
		t.Fatal("descriptor not created")
	}
	first := append([]byte(nil), session.commentary.content...)
	proxy.commentary.mu.Lock()
	clear(proxy.commentary.routes)
	proxy.commentary.mu.Unlock()
	proxy.prepareShellCommentary("commentary-thread", "history-new", "")
	if string(first) == string(session.commentary.content) {
		t.Fatal("expired capability not refreshed")
	}
	content, _, err := readShellCommentary(session.parent, session.commentary.name)
	if err != nil || string(content) != string(session.commentary.content) {
		t.Fatal("refreshed descriptor unavailable")
	}
	name := session.commentary.name
	if err := proxy.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(proxy.shellDirectory, name)); !os.IsNotExist(err) {
		t.Fatal("descriptor survived close")
	}
}

func TestShellWorkerDiscoversThreadJournal(t *testing.T) {
	registry, _ := newToolPluginTestRegistry(t)
	proxy, _ := newShellStorageTestProxy(t)
	proxy.registry = registry
	proxy.journals = newJournalStore()
	if err := proxy.journals.initialize(t.Context(), nil, "", "worker-thread", "/root", ""); err != nil {
		t.Fatal(err)
	}

	proxy.commentary = newCommentaryBroker()
	proxy.commentary.journalPublisher = func(ctx context.Context, _, thread, receipt string, mutations []journalMutation) ([]string, error) {
		return proxy.journals.apply(ctx, nil, "", thread, receipt, mutations)
	}

	server := httptest.NewServer(http.HandlerFunc(proxy.commentary.serveHTTP))
	defer server.Close()
	proxy.commentaryEndpoint = server.URL
	if _, err := proxy.storeShellRuntime("worker-thread"); err != nil {
		t.Fatal(err)
	}
	proxy.prepareShellCommentary("worker-thread", "worker-history", "")
	t.Setenv(shellruntime.RuntimeDirectoryEnvironment, proxy.shellDirectory)
	t.Setenv(shellruntime.ThreadIDEnvironment, "worker-thread")
	for _, value := range []string{"expanded", "I’ll remove the generated collaboration-call notices and forward the subagents’ own progress to the main conversation instead."} {
		var stdout, stderr bytes.Buffer
		handled, code := RunToolPluginWorker(t.Context(), registry.shellRuntime, []string{"bash", "--", value, "journal add \"$1\"; sleep 0.01; journal add \"completed $1\"; printf stdout; printf stderr >&2; exit 7"}, os.Stdin, &stdout, &stderr)
		if !handled || code != 7 || stdout.String() != "stdout" || stderr.String() != "stderr" {
			t.Fatalf("worker handled=%v code=%d stdout=%q stderr=%q", handled, code, stdout.String(), stderr.String())
		}
		items, err := proxy.journals.list(t.Context(), nil, "", "worker-thread")
		if err != nil || len(items) < 2 || items[len(items)-2].Text != value || items[len(items)-1].Text != "completed "+value {
			t.Fatalf("journal = %+v, error = %v", items, err)
		}
	}
}
