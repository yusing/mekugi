//go:build codex_e2e

package launch

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/composition"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestNativeCompactionDeliveryLatency(t *testing.T) {
	for _, records := range []int{128, 120000} {
		t.Run(fmt.Sprint(records), func(t *testing.T) {
			testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{compactionRecords: records})
		})
	}
	t.Run("nonGit", func(t *testing.T) {
		testNativeThreadsServerOwnedCodex(t, nativeThreadsScenario{compactionRecords: 128, nonGit: true})
	})
}

// The server runs in another process. This writer keeps publishing real peer
// revisions throughout the checkpoint, so no warmed process-local cache can
// conceal corpus-sized admission work.
func prepareCompactionLatencyStore(t *testing.T, directory string, records int) func() func() {
	t.Helper()
	runtime, err := composition.Frontends(t.Context(), false, true, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.WithoutCancel(t.Context())) })
	store, err := runtime.History.Open(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	for i := range records {
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("threadcontrol-import-latency-%064x", i)), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Measure ordinary admitted work separately from initial store migration.
	if err := store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		return tx.Admit(history.Limits{}, "threadcontrol-import-latency-peer", 0)
	}); err != nil {
		t.Fatal(err)
	}
	return func() func() {
		ctx, cancel := context.WithCancel(t.Context())
		joined := make(chan error, 1)
		go func() {
			ticker := time.NewTicker(25 * time.Millisecond)
			defer ticker.Stop()
			for i := 0; ; i++ {
				select {
				case <-ctx.Done():
					joined <- nil
					return
				case <-ticker.C:
				}
				err := store.Transaction(ctx, nil, func(tx *history.Transaction) error {
					return tx.Write("threadcontrol-import-latency-peer", "latency-pending-", []byte(fmt.Sprint(i)))
				})
				if err != nil {
					joined <- err
					return
				}
			}
		}()
		return func() {
			cancel()
			if err := <-joined; err != nil && !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		}
	}
}

func measureCompactionDelivery(t *testing.T, connection *sessionapi.NativeConnection, fixture *nativeThreadsProvider) {
	t.Helper()
	views, err := composition.NativeRenderer(true)
	if err != nil {
		t.Fatal(err)
	}
	terminal, _ := views.NewAttachment(diffrender.DarkTheme)
	t.Cleanup(func() { _ = terminal.Diff.Close() })
	renderer := nativeClientRenderer{terminal: terminal, views: views}
	var compacting, requested, reset time.Time
	var rendered string
	for {
		event, err := connection.Next()
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		renderer.update(event.Frame, event.Frame.View.Draft, event.Frame.View.PromptDraft, now)
		frame := terminal.Main.Render(terminal.Transcript, &terminal.Transcript.Painter, terminal.Prompts, terminalsession.MainOptions{Now: now, Focused: true, Version: mekugiVersion()}, 120, 32, 0)
		rendered = strings.Join(frame.Rows, "\n")
		if compacting.IsZero() && strings.Contains(rendered, "Compacting context") {
			compacting = time.Now()
		}
		if requested.IsZero() && strings.Contains(rendered, "Compaction requested") {
			requested = time.Now()
		}
		if reset.IsZero() && strings.Contains(rendered, "Context reset") {
			reset = time.Now()
		}
		if strings.Contains(rendered, "MANAGED_PARENT_RESUMED") {
			break
		}
	}
	fixture.mu.Lock()
	issued, continued, calls := fixture.compactIssued, fixture.compactContinued, fixture.calls[fixture.parent]
	fixture.mu.Unlock()
	if issued.IsZero() || continued.IsZero() || requested.IsZero() || reset.IsZero() || calls != 2 {
		t.Fatalf("missing checkpoint delivery: requested=%v reset=%v issued=%v continued=%v calls=%d\n%s", requested, reset, issued, continued, calls, rendered)
	}
	if !compacting.IsZero() {
		t.Logf("tool emitted -> compacting rendered: %s", compacting.Sub(issued))
	} else {
		t.Log("transient Compacting context frame was not sampled")
	}
	t.Logf("tool emitted -> requested rendered: %s; -> reset rendered: %s; -> continuation inference: %s; requested -> reset rendered: %s", requested.Sub(issued), reset.Sub(issued), continued.Sub(issued), reset.Sub(requested))
	if export := os.Getenv("MEKUGI_TEST_LIFECYCLE_EXPORT"); export != "" {
		path := filepath.Join(export, t.Name(), "rendered.txt")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(rendered+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("rendered result: %s", path)
	}
}

func reportCompactionRollout(t *testing.T, directory string) {
	t.Helper()
	err := filepath.WalkDir(filepath.Join(directory, "sessions"), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for line := range bytes.SplitSeq(data, []byte("\n")) {
			var row struct {
				Timestamp string `json:"timestamp"`
				Type      string `json:"type"`
				Payload   struct {
					Type string `json:"type"`
				} `json:"payload"`
			}
			if len(line) == 0 {
				continue
			}
			if err := json.Unmarshal(line, &row); err != nil {
				return err
			}
			if row.Type == "compacted" || row.Payload.Type == "task_started" || row.Payload.Type == "task_complete" || row.Payload.Type == "custom_tool_call" || row.Payload.Type == "custom_tool_call_output" {
				t.Log(row.Timestamp, row.Type, row.Payload.Type)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
