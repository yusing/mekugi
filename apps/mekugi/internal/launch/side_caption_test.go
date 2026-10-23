package launch

import (
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/inferencegateway"
	"github.com/yusing/mekugi/contracts/inference"
	"github.com/yusing/mekugi/modules/usage/accounting"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotSideComposerMetrics(t *testing.T) {
	for _, command := range []string{"/side", "/oneoff inspect"} {
		t.Run(command, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				u, w := newAppServerTestUI()
				u.startSession("main", "/workspace")
				u.model, u.reasoningEffort, u.serviceTier = "main-model", "high", "priority"
				u.usage = accounting.New()
				t.Cleanup(u.usage.Close)
				u.clock = func() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.Local) }
				appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": "main", "tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 3000}, "modelContextWindow": 10000}})
				appServerTestKeys(t, u, command+"\r")
				fork := btwTestRequest(t, w, "thread/fork", "main")
				btwTestReply(t, u, fork, `{"thread":{"id":"side"},"model":"side-model","reasoningEffort":"low","serviceTier":"flex"}`)
				if command == "/side" {
					appServerTestKeys(t, u, "question\r")
				}
				start := btwTestRequest(t, w, "turn/start", "side")
				btwTestReply(t, u, start, `{"turn":{"id":"side-turn"}}`)
				u.testMainFrame(120, 12, 0)
				if caption := u.attached.presentation.View().Caption; caption.Model != "side-model" || caption.Effort != "low" || caption.Tier != "flex" {
					t.Fatalf("fork settings lost: %+v", caption)
				}
				measured := u.usage.Observation("side", "side", "side-model", "flex")
				measured.Begin()
				time.Sleep(2 * time.Second)
				measured.Observe(inference.Usage{TotalsKnown: true, OutputTokens: 100})
				appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": "side", "turnId": "side-turn", "tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 8000}, "modelContextWindow": 20000}})
				appServerTestNotify(t, u, "thread/settings/updated", map[string]any{"threadId": "side", "threadSettings": map[string]any{"model": "live-side", "effort": "medium", "serviceTier": nil}})
				u.serviceTiers = inferencegateway.NewTiers(map[string]string{"live-side": "priority"})
				u.serviceTiers.Confirm("live-side", "side", "flex")
				for _, width := range []int{48, 120} {
					rows, _ := u.testMainFrame(width, 12, 0)
					caption := u.attached.presentation.View().Caption
					if caption.Model != "live-side" || caption.Effort != "medium" || caption.Tier != "flex" || !caption.ContextKnown || caption.ContextTokens != 8000 || caption.ContextWindow != 20000 || len(caption.Items) == 0 {
						t.Fatalf("Side metrics lost: %+v", caption)
					}
					if strings.Contains(ansi.Strip(rows[len(rows)-1]), "main-model") {
						t.Fatal("Side borrowed Main's settings")
					}
					uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/side-composer-metrics-%s-%d.txt", u.side.Label(), width), rows, width)
				}
				selectSideThread(t, u, u.attached.presentation.View().Main)
				u.testMainFrame(120, 12, 0)
				if caption := u.attached.presentation.View().Caption; caption.Model != "main-model" || caption.ContextTokens != 3000 || len(caption.Items) != 0 {
					t.Fatalf("Side metrics leaked to Main: %+v", caption)
				}
				selectSideThread(t, u, u.attached.presentation.View().SideThread)
				composerShellEscape(t, u)
				appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": "side", "tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 9999}}})
				if u.attached.session.KnownCaller("side") != "" {
					t.Fatal("closed side retained metrics")
				}
			})
		})
	}
}
