package router

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
)

func TestUISnapshotCommandSegmentRetentionNotice(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"command-segments-storage-busy", context.DeadlineExceeded},
		{"command-segments-storage-failure", storageIOError(errors.New("open /workspace/replay/call-record.json: permission denied\n" + strings.Repeat("diagnostic detail ", 12) + "final-cause-marker"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.view.painter.Theme = livediff.DarkTheme
			u.clock = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local) }
			u.thread = "main"
			u.view.conversation = true
			u.commandSegmentPending = 1
			u.commandSegmentRetained(commandSegmentWrite{key: [3]string{"main", "turn", "command"}, err: tc.err})
			delivery := u.applyCriticalNotices()
			if delivery == nil || !strings.Contains(u.notice, tc.err.Error()) {
				t.Fatal("composer omitted the underlying error")
			}
			rows, _ := u.mainFrame(80, 12, 0)
			assertNativeUISnapshot(t, "command-segments-storage-failure", rows)
			delivery.finish(true)
		})
	}
}
