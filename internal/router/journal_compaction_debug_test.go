package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	json "encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
)

func TestJournalCompactionFallbackDebug(t *testing.T) {
	for _, mode := range []string{"auto", "slice", "lease-error"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			flags := newRouterFlags(io.Discard)
			*flags.debug = true
			debug, err := openDebugOutput(flags)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := debug.close(); err != nil {
					t.Error(err)
				}
			})
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			thread := transform.shellThreadID
			proxy.journalCompaction = "auto"
			if mode == "slice" {
				proxy.journalCompaction = mode
			}
			if mode == "lease-error" {
				thread = "lease-error-thread"
				lease := strings.TrimSuffix(storageSessionName(thread), ".json") + ".lock"
				if err := os.Mkdir(filepath.Join(proxy.replayStore.directory, lease), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "auto" {
				if err := proxy.journals.transaction(transform.ctx, proxy.replayStore, workspace, thread, func(j *threadJournal, _ bool) error {
					j.IdentityConflicted = true
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			var notices [][4]string
			proxy.noticeSink = func(session, thread, category, message string) {
				notices = append(notices, [4]string{session, thread, category, message})
			}
			request, headers := journalCompactionRequest(t, workspace, thread)
			original, err := request.wireBody(request.fields)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := journalCompactionSSE("provider-compact", "gpt-test", "Provider recovery")
			if err != nil {
				t.Fatal(err)
			}
			response := serverHTTPResponse(string(wire))
			response.Header.Set("Content-Type", "text/event-stream")
			provider := &serverFakeProvider{results: []serverForwardResult{{response: response}}}
			ctx := context.WithValue(t.Context(), debugContextKey{}, debug)
			var output bytes.Buffer
			if err := executeRequest(ctx, ctx, request, headers, "compact-debug", provider, &output, nil, proxy); err != nil {
				t.Fatal(err)
			}
			if len(provider.forwarded) != 1 || !bytes.Equal(provider.forwarded[0], original) || !bytes.Equal(output.Bytes(), wire) {
				t.Fatalf("fallback altered provider request/response: forwarded=%q output=%s", provider.forwarded, &output)
			}
			data, err := os.ReadFile(debug.paths[0])
			if err != nil {
				t.Fatal(err)
			}
			var fallbacks []map[string]any
			var requestID string
			for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte("\n")) {
				var event map[string]any
				if err := json.Unmarshal(line, &event); err != nil {
					t.Fatal(err)
				}
				switch event["event"] {
				case "journal_compaction_fallback":
					fallbacks = append(fallbacks, event)
				case "request_complete":
					requestID, _ = event["request_id"].(string)
				}
			}
			if mode == "slice" {
				if len(fallbacks) != 0 || len(notices) != 0 {
					t.Fatalf("unchanged slice produced fallback diagnostics: events=%v notices=%v", fallbacks, notices)
				}
				return
			}
			if len(fallbacks) != 1 {
				t.Fatalf("want one fallback event, got %v", fallbacks)
			}
			event := fallbacks[0]
			cause := "journal summary identity is unavailable or conflicted"
			if mode == "lease-error" {
				cause = "session storage lease is not a regular file"
			}
			if event["error"] != cause || requestID == "" || event["request_id"] != requestID || event["session_id"] != "compact-debug" || event["thread_id"] != thread {
				t.Fatalf("fallback diagnostic lost error or request identity: %v; request=%q", event, requestID)
			}
			wantNotice := [4]string{"compact-debug", thread, fmt.Sprintf("journal_compaction_fallback:%x", sha256.Sum256([]byte(cause))), "Journal compaction unavailable; using the provider summary. Error: " + cause}
			if len(notices) != 1 || notices[0] != wantNotice {
				t.Fatalf("fixed fallback notice changed: %v", notices)
			}
		})
	}
}

func TestUISnapshotJournalCompactionFallback(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.issues = NewCriticalErrors()
	u.view.painter.Theme = livediff.DarkTheme
	u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
	u.thread, u.view.conversation = "main", true
	proxy := &mekugiProxy{noticeSink: u.issues.addThreadNotice}
	attempt := requestAttempt{executor: requestExecutor{mekugiCalls: proxy}, threadID: "main"}
	attempt.journalCompactionFallback(fmt.Errorf("session storage lease is not a regular file"))
	delivery := u.applyCriticalNotices()
	if delivery == nil {
		t.Fatal("fallback cause did not reach the composer")
	}
	rows, _ := u.mainFrame(80, 12, 0)
	assertNativeUISnapshot(t, "journal-compaction-fallback", rows)
	delivery.finish(true)
}
