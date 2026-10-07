//go:build unix

package router

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/session"
)

func awaitNativeNoticeFrame(t *testing.T, terminal *nativeClaudePTY, timeout time.Duration, match func(string) bool) {
	t.Helper()
	if !terminal.observe(timeout, match) {
		t.Fatalf("native notice state did not arrive:\n%s", terminal.screen.String())
	}
}

func TestNativeRuntimeNoticesPTYCompactDeliveryAndDismissal(t *testing.T) {
	t.Parallel()
	// Use a real failed retention write to populate the loop's shared queue.
	// The completed observer report is fixed before the UI starts.
	u, track := runtimeSegmentUI(t)
	track.apply(execsegment.Message{Type: execsegment.End, Index: 1, Code: new(1)})
	track.apply(execsegment.Message{Type: execsegment.Done, Code: new(1)})
	track.ended = true
	binding := ObservationBinding{Runtime: "claude", Session: u.thread, Workspace: t.TempDir()}
	directory := t.TempDir()
	service, _, _ := observationIsolationService(t, directory, binding)
	if err := service.owner.bind(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	service.owner.execTrack = u.execTrack
	key := "claude/" + commandSegmentsID(binding.Session, "command")
	if err := os.Mkdir(filepath.Join(directory, replayRecordName(binding.Workspace, key, false)), 0700); err != nil {
		t.Fatal(err)
	}
	client := &runtimeTestClient{events: make(chan session.Event, 16)}
	terminal, stop := startNativeRuntimePTY(t, t.Context(), client, binding.Workspace, service)
	t.Cleanup(stop)
	terminal.resize(120, 5)
	client.events <- session.Event{Kind: "session", SessionID: binding.Session}
	client.events <- session.Event{Kind: "ready"}
	client.events <- session.Event{Kind: "tool", ID: "command", Role: "Bash", Text: savedSegmentInput}
	client.events <- session.Event{Kind: "tool_result", ID: "command", Text: "first\n", Failed: true}
	const notice = "Command segment history could not be retained"
	awaitNativeNoticeFrame(t, terminal, 3*time.Second, func(frame string) bool { return strings.Contains(frame, notice) })
	if strings.Contains(terminal.screen.String(), "first") {
		t.Fatal("compact fixture painted the transcript")
	}
	// Click the rendered check, without editing (which also clears feedback).
	clicked := false
	for y, row := range strings.Split(terminal.screen.String(), "\n") {
		if x := strings.Index(row, "[✓]"); x >= 0 {
			terminal.keys(fmt.Sprintf("\x1b[<0;%d;%dM", ansi.StringWidth(row[:x])+2, y+1))
			clicked = true
			break
		}
	}
	if !clicked {
		t.Fatal("visible error has no dismiss control")
	}
	awaitNativeNoticeFrame(t, terminal, 3*time.Second, func(frame string) bool { return !strings.Contains(frame, notice) })
	// A fresh full-height paint must not reclaim an already delivered error.
	terminal.resize(120, 36)
	client.events <- session.Event{Kind: "message", ID: "after-dismissal", Role: "Claude", Text: "AFTER_NOTICE_DISMISSAL"}
	awaitNativeNoticeFrame(t, terminal, 3*time.Second, func(frame string) bool { return strings.Contains(frame, "AFTER_NOTICE_DISMISSAL") })
	if strings.Contains(terminal.screen.String(), notice) {
		t.Fatal("compact delivery left the shared error pending after dismissal")
	}
}

func TestNativeRuntimeNoticesPTYTransientExpiryPreservesDraft(t *testing.T) {
	t.Parallel()
	client := &runtimeTestClient{events: make(chan session.Event, 16)}
	terminal, stop := startNativeRuntimePTY(t, t.Context(), client, t.TempDir(), nil)
	t.Cleanup(stop)
	client.events <- session.Event{Kind: "ready"}
	awaitNativeNoticeFrame(t, terminal, 3*time.Second, func(frame string) bool { return strings.Contains(frame, "Ready") })
	terminal.keys("DRAFT_SURVIVES_EXPIRY")
	awaitNativeNoticeFrame(t, terminal, 3*time.Second, func(frame string) bool { return strings.Contains(frame, "DRAFT_SURVIVES_EXPIRY") })
	client.events <- session.Event{Kind: "notice", Text: "TRANSIENT_NATIVE_FEEDBACK"}
	awaitNativeNoticeFrame(t, terminal, 3*time.Second, func(frame string) bool { return strings.Contains(frame, "TRANSIENT_NATIVE_FEEDBACK") })
	// The native notice event also enters the transcript. Only the composer
	// border should expire, with no new input/event needed to cause a paint.
	awaitNativeNoticeFrame(t, terminal, 5*time.Second, func(frame string) bool {
		return strings.Count(frame, "TRANSIENT_NATIVE_FEEDBACK") == 1 && strings.Contains(frame, "DRAFT_SURVIVES_EXPIRY")
	})
}
