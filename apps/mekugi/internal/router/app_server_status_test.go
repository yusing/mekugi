package router

import (
	"encoding/base64"
	"encoding/json/jsontext"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/diffrender"
)

func TestUISnapshotAppServerStatusResetCredits(t *testing.T) {
	// Recorded from codex-cli 0.162.1 using isolated fake ChatGPT auth and local
	// backend fixtures. The host performed its authenticated usage and detail GETs.
	wire, err := os.ReadFile("testdata/status/reset-credits-wire.json")
	if err != nil {
		t.Fatal(err)
	}
	previous := time.Local
	time.Local = time.FixedZone("fixture", 8*60*60)
	t.Cleanup(func() { time.Local = previous })
	for _, tc := range []struct {
		name, summary string
	}{
		{"wire", ""},
		{"count-only", `{"availableCount":3,"credits":null}`},
		{"unavailable", `null`},
		{"empty", `{"availableCount":0,"credits":[]}`},
		{"partial", `{"availableCount":4,"credits":[{"resetType":"codexRateLimits","status":"available","expiresAt":null,"title":null},{"resetType":"unknown","status":"available","expiresAt":null,"title":"Weekly reset"},{"resetType":"unknown","status":"available","expiresAt":null,"title":null},{"resetType":"codexRateLimits","status":"redeemed","title":"Consumed"},{"resetType":"codexRateLimits","status":"unknown","title":"Unconfirmed"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, sent := newAppServerTestUI()
			u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
			appServerTestKeys(t, u, "/status\r")
			appServerTestMessage(t, u, `{"id":1,"result":{"account":{"type":"chatgpt","planType":"pro"}}}`)
			response := string(wire)
			if tc.summary != "" {
				response = `{"id":2,"result":{"rateLimits":{"primary":{"usedPercent":20}},"rateLimitResetCredits":` + tc.summary + `}}`
			}
			appServerTestMessage(t, u, response)
			if u.Requests.Pending() || len(u.attached.source.Entries) != 0 || strings.Count(sent.String(), `"method"`) != 2 || !strings.Contains(sent.String(), `"method":"account/rateLimits/read"`) {
				t.Fatalf("status changed read-only request flow: %s", sent.String())
			}
			assertNativeUISnapshot(t, "native-status-resets-"+tc.name, u.statusPanelFrame(80, 40))
			if tc.name == "wire" {
				assertNativeUISnapshot(t, "native-status-resets-narrow", u.statusPanelFrame(44, 40))
			}
		})
	}
}

func TestUISnapshotAppServerStatusSystemTimezone(t *testing.T) {
	// CST is ambiguous: Taipei and Chicago share the abbreviation, not the offset.
	previous := time.Local
	t.Cleanup(func() { time.Local = previous })
	for _, tc := range []struct {
		name, zone, expected string
		reset                time.Time
	}{
		{"taipei", "Asia/Taipei", "Jan 2 20:00 UTC+08:00", time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)},
		{"chicago-winter", "America/Chicago", "Jan 2 06:00 UTC-06:00", time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)},
		{"chicago-summer", "America/Chicago", "Jul 2 07:00 UTC-05:00", time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)},
		{"utc", "UTC", "Jan 2 12:00 UTC+00:00", time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			time.Local, err = time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			u, _ := newAppServerTestUI()
			u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
			appServerTestKeys(t, u, "/status\r")
			appServerTestMessage(t, u, `{"id":1,"result":{"account":{"type":"chatgpt","planType":"pro"}}}`)
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":2,"result":{"rateLimits":{"primary":{"usedPercent":20,"windowDurationMins":300,"resetsAt":%d}}}}`, tc.reset.Unix()))
			rows := u.statusPanelFrame(80, 24)
			if body := ansi.Strip(strings.Join(rows, "\n")); !strings.Contains(body, "resets "+tc.expected) {
				t.Fatalf("reset did not follow system timezone: %s", body)
			}
			assertNativeUISnapshot(t, "native-status-"+tc.name, rows)
		})
	}
}

func TestAppServerStatusLocalAndAccountRefresh(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")

	u.model, u.reasoningEffort, u.serviceTier = "gpt-6-sol", "high", "priority"
	u.Turn, u.attached.presentation.Status.Text = "active", "Working"
	u.statusConfig.Provider = "openai"
	u.statusConfig.Approval = jsontext.Value(`"never"`)
	u.statusConfig.Sandbox.Type = "danger-full-access"
	u.statusConfig.Instructions = []string{"/workspace/AGENTS.md"}
	appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": "main", "tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 1200, "outputTokens": 30}, "last": map[string]any{"totalTokens": 1000}, "modelContextWindow": 4000}})
	appServerTestKeys(t, u, "/status\r")
	if len(u.attached.source.Entries) != 0 || u.Text != "" || u.Turn != "active" || u.attached.presentation.Status.Text != "Working" {
		t.Fatalf("status altered session: draft=%q turn=%q status=%q entries=%d", u.Text, u.Turn, u.attached.presentation.Status.Text, len(u.attached.source.Entries))
	}
	for _, want := range []string{"Model: gpt-6-sol", "Reasoning: high", "Service tier: priority", "Directory: /workspace", "Approvals: never", "Sandbox: danger-full-access", "Agents.md: /workspace/AGENTS.md", "75% left", "Loading account"} {
		if !strings.Contains(statusReportText(u.statusPanel), want) {
			t.Fatalf("missing %q in %s", want, statusReportText(u.statusPanel))
		}
	}
	if strings.Count(wire.String(), "\n") != 1 || !strings.Contains(wire.String(), `"method":"account/read"`) || !strings.Contains(wire.String(), `"refreshToken":false`) {
		t.Fatalf("not a local command: %s", wire.String())
	}
	appServerTestMessage(t, u, `{"id":1,"result":{"account":{"type":"chatgpt","email":"user@example.com","planType":"plus"}}}`)
	if !strings.Contains(wire.String(), "account/rateLimits/read") {
		t.Fatal("missing limits read")
	}
	// Force cached rendering before the asynchronous replacement.
	u.statusPanelFrame(70, 60)
	appServerTestMessage(t, u, `{"id":2,"result":{"rateLimits":{"primary":{"usedPercent":99}},"rateLimitsByLimitId":{"codex":{"limitName":"Codex","primary":{"usedPercent":20,"windowDurationMins":300,"resetsAt":1800000000},"secondary":{"usedPercent":100,"windowDurationMins":10080},"credits":{"unlimited":false,"balance":"12.50"}}}}}`)
	body := ansi.Strip(strings.Join(u.statusPanelFrame(70, 60), "\n"))
	for _, want := range []string{"user@example.com", "plus", "Codex 5h limit", "80% left", "Codex weekly limit", "0% left", "resets", "12.50"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in rendered card: %s", want, body)
		}
	}
	if strings.Contains(body, "Token usage:") || strings.Contains(body, "Loading") || len(u.attached.source.Entries) != 0 || u.Requests.Pending() {
		t.Fatalf("stale/duplicated card: %s", body)
	}
}

func TestAppServerStatusIndependentSnapshotsAndFailure(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.model = "first"
	appServerTestKeys(t, u, "/status\r")
	firstPanel := u.statusPanel
	u.statusPanelKey("\x1b")
	u.model = "second"
	appServerTestKeys(t, u, "/status\r")
	appServerTestMessage(t, u, `{"id":2,"result":{"account":{"type":"apiKey"}}}`)
	appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"offline"}}`)
	if len(u.attached.source.Entries) != 0 {
		t.Fatalf("cards=%d", len(u.attached.source.Entries))
	}
	first, second := statusReportText(firstPanel), statusReportText(u.statusPanel)
	if !strings.Contains(first, "Model: first") || strings.Contains(first, "offline") || strings.Contains(first, "API key") || !strings.Contains(second, "Model: second") || !strings.Contains(second, "API key") {
		t.Fatalf("crossed replies: %q / %q", first, second)
	}
	if u.attached.presentation.Status.Alert || u.attached.presentation.Status.Text != "" || u.Requests.Pending() {
		t.Fatal("optional failure damaged session or retained request")
	}
	for _, card := range []string{first, second} {
		if strings.Contains(card, "Token usage:") || strings.Contains(card, "Context window:") {
			t.Fatalf("invented usage: %s", card)
		}
	}
}

func TestAppServerStatusUsesCurrentHostSettings(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.startSession("main", "/old")
	for _, thread := range []string{"main", "child"} {
		appServerTestNotify(t, u, "thread/settings/updated", map[string]any{"threadId": thread, "threadSettings": map[string]any{"model": thread, "modelProvider": thread, "approvalPolicy": "on-request", "sandboxPolicy": map[string]any{"type": "read-only"}, "cwd": "/" + thread}})
	}
	appServerTestKeys(t, u, "/status\r")
	appServerTestMessage(t, u, `{"id":1,"result":{"account":null}}`)
	body := statusReportText(u.statusPanel)
	for _, want := range []string{"Model: main", "Model provider: main", "Directory: /main", "Approvals: on-request", "Sandbox: read-only"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q: %s", want, body)
		}
	}
}

func TestAppServerStatusInvalidOptionalResponses(t *testing.T) {
	for _, response := range []string{`{"account":false}`, `{"account":{"type":"apiKey"}}`} {
		u, wire := newAppServerTestUI()
		u.startSession("main", "/work")
		u.attached.session.Agent("/root").InputTokens = 12
		appServerTestKeys(t, u, "/status\r")
		appServerTestMessage(t, u, `{"id":1,"result":`+response+`}`)
		if u.Requests.Pending() || strings.Contains(statusReportText(u.statusPanel), "Loading") || strings.Contains(wire.String(), "rateLimits") {
			t.Fatal("optional result left pending or fetched unsupported limits")
		}
		if strings.Contains(response, "apiKey") && !strings.Contains(statusReportText(u.statusPanel), "Token usage: 12 input") {
			t.Fatal("API token usage missing")
		}
	}
}

func TestAppServerStatusPanelCloseScrollAndLateResponse(t *testing.T) {
	for _, closeKey := range []string{"\x1b", "\r", "q", "\x03"} {
		t.Run(closeKey, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.Turn, u.attached.presentation.Status.Text = "active", "Working"
			u.testActivity(u.shell.Transcript).Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "text", Text: "Existing transcript"}}})
			shell := terminalTest(u)
			shell.Layout.Main = terminalsession.Rect{0, 0, 60, 12}
			appServerTestKeys(t, u, "/status\r")
			u.statusPanel.Body = nil
			for range 40 {
				u.statusPanel.Body = append(u.statusPanel.Body, sessionpresentation.StatusField{Group: "Session", Value: "long status row"})
			}
			u.statusPanel.Render(statusNotice("Loading account…"))
			u.dirty = true
			for _, width := range []int{1, 12, 60} {
				frame, _ := u.testMainFrame(width, 8, 0)
				for _, row := range frame {
					if ansi.StringWidth(row) > width {
						t.Fatalf("overflow at %d: %q", width, row)
					}
				}
				if u.shell.Main.ContentPainted {
					t.Fatal("covered transcript acknowledged")
				}
			}
			if err := shell.send("\x1b[6~"); err != nil {
				t.Fatal(err)
			}
			if u.shell.Main.Panel.Top == 0 {
				t.Fatal("panel did not scroll")
			}
			before := u.shell.Main.Panel.Top
			if err := shell.mouse("\x1b[<65;2;3M"); err != nil {
				t.Fatal(err)
			}
			if u.shell.Main.Panel.Top != before+1 {
				t.Fatal("wheel did not scroll panel")
			}
			if err := shell.send(closeKey); err != nil {
				t.Fatal(err)
			}
			if u.statusPanel != nil || u.Turn != "active" || u.attached.presentation.Status.Text != "Working" || len(u.attached.source.Entries) != 1 || u.attached.source.Entries[0].Source.Text != "Existing transcript" {
				t.Fatal("closing changed turn or transcript")
			}
			// A response to a closed panel must not fetch limits or resurrect UI.
			appServerTestMessage(t, u, `{"id":1,"result":{"account":{"type":"chatgpt","planType":"plus"}}}`)
			if u.statusPanel != nil || u.Requests.Pending() || strings.Contains(wire.String(), "rateLimits") || strings.Contains(wire.String(), "turn/") {
				t.Fatalf("closed panel performed work: %s", wire.String())
			}
		})
	}
}

func TestAppServerStatusRefreshFailureStaysInPanel(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.attached.presentation.Status.Text = "Working"
	appServerTestKeys(t, u, "/status\r")
	appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"offline"}}`)
	if !strings.Contains(statusReportText(u.statusPanel), "offline") || u.attached.presentation.Status.Alert || u.attached.presentation.Status.Text != "Working" || len(u.attached.source.Entries) != 0 {
		t.Fatal("optional failure altered session")
	}
}

func TestAppServerStatusTerminalPasteCanClose(t *testing.T) {
	u, _ := newAppServerTestUI()
	shell := terminalTest(u)
	for _, key := range []byte("/status\r\x1b[200~q\r\x03\x1b[B\x1b[201~") {
		if err := shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if u.statusPanel == nil || u.Text != "" || shell.Decoder.Pasting() {
		t.Fatal("paste affected panel or composer")
	}
	if err := shell.key('q'); err != nil {
		t.Fatal(err)
	}
	if u.statusPanel != nil || u.quitRequested || len(u.attached.source.Entries) != 0 {
		t.Fatal("panel trapped input after paste")
	}
}

func statusReportText(p *appServerStatusReport) string {
	var rows []string
	for _, f := range p.Fields {
		text := f.Value
		if f.Label != "" {
			text = f.Label + ": " + text
		}
		rows = append(rows, text)
	}
	return strings.Join(rows, "\n")
}

func TestAppServerStatusPanelVisual(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.model, u.reasoningEffort, u.serviceTier = "gpt-6-sol", "high", "priority"
	u.startSession("main", "/home/yusing/projects/mekugi")
	u.statusConfig.Provider = "openai"
	u.statusConfig.Approval = jsontext.Value(`"never"`)
	u.statusConfig.Sandbox.Type = "danger-full-access"
	u.statusConfig.Instructions = []string{"/home/yusing/projects/mekugi/AGENTS.md"}
	root := u.attached.session.Agent("/root")
	root.ContextKnown, root.ContextTokens, root.ContextWindow = true, 120000, 400000
	appServerTestKeys(t, u, "/status\r")
	appServerTestMessage(t, u, `{"id":1,"result":{"account":{"type":"chatgpt","email":"yusing@example.com","planType":"pro"}}}`)
	appServerTestMessage(t, u, `{"id":2,"result":{"rateLimits":{"primary":{"usedPercent":22,"windowDurationMins":300},"secondary":{"usedPercent":83,"windowDurationMins":10080},"credits":{"unlimited":false,"balance":"12.50"}}}}`)
	for _, theme := range []diffrender.Theme{diffrender.DarkTheme, diffrender.LightTheme} {
		u.shell.Transcript.Painter.Theme = theme
		for _, size := range [][2]int{{88, 42}, {44, 20}, {12, 8}, {1, 1}} {
			frame := u.statusPanelFrame(size[0], size[1])
			for _, row := range frame {
				if ansi.StringWidth(row) > size[0] {
					t.Fatalf("overflow %v: %q", size, row)
				}
			}
			if size[0] >= 12 {
				var edgeWidth int
				for _, row := range frame {
					plain := strings.TrimSpace(ansi.Strip(row))
					if strings.HasPrefix(plain, "│") {
						if !strings.HasSuffix(plain, "│") {
							t.Fatalf("missing right edge: %q", plain)
						}
						if edgeWidth != 0 && ansi.StringWidth(plain) != edgeWidth {
							t.Fatalf("misaligned edge: %q", plain)
						}
						edgeWidth = ansi.StringWidth(plain)
					}
					if strings.HasPrefix(plain, "╰") && !strings.HasSuffix(plain, "╯") {
						t.Fatalf("missing bottom corner at width %d: %q", size[0], plain)
					}
				}
			}
			if size[0] == 88 {
				text := strings.Join(frame, "\n")
				plain := ansi.Strip(text)
				if !strings.Contains(plain, "╭") || !strings.Contains(plain, "╯") || !strings.Contains(plain, "━━") || !strings.Contains(plain, "78% left") || !strings.Contains(plain, "17% left") {
					t.Fatalf("missing panel chrome or gauges:\n%s", plain)
				}
				if !strings.Contains(text, "\x1b[1m") || !strings.Contains(text, "\x1b[38;2;") {
					t.Fatal("status has no visual hierarchy")
				}

			}
		}
	}

	// Render through the real pane compositor and terminal emulator, then verify
	// closing clears the already-painted panel, not only its backing state.
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	u.shell.Main.Panel.Top = 0
	screen := vt.NewEmulator(160, 48)
	defer screen.Close()
	if err := u.testPaint(screen, 160, 48); err != nil {
		t.Fatal(err)
	}

	defer terminalTest(u).Diff.Close()
	rendered := screen.String()
	for _, want := range []string{"Session status", "gpt-6-sol", "78% left", "17% left", "━━", "Esc close"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("compositor lost %q:\n%s", want, rendered)
		}
	}
	r := terminalTest(u).Layout.Main
	var panel []string
	for _, row := range strings.Split(rendered, "\n")[r.Y : r.Y+r.H] {
		panel = append(panel, ansi.Cut(row, r.X, r.X+r.W))
	}
	t.Log("Rendered Main pane:\n" + strings.Join(panel, "\n"))
	saved := u.statusPanel
	if err := terminalTest(u).send("\x1b"); err != nil {
		t.Fatal(err)
	}
	if err := u.testPaint(screen, 160, 48); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(screen.String(), "Session status") || strings.Contains(screen.String(), "78% left") {
		t.Fatal("closed panel left painted residue")
	}
	u.statusPanel = newStatusReport(u.attached.presentation, u.replayDebugDirectory)
	u.statusPanel.Fields = saved.Fields
	// A long off-screen label cannot change the visible value alignment.
	u.shell.Main.Panel.Top = 0
	before := strings.Join(u.statusPanelFrame(72, 12), "\n")
	u.statusPanel.Fields = append(u.statusPanel.Fields, sessionpresentation.StatusField{Group: "Permissions", Label: "Offscreen", Value: "not visible"})
	after := strings.Join(u.statusPanelFrame(72, 12), "\n")
	// Only the footer's total line count may differ.
	beforeRows, afterRows := strings.Split(before, "\n"), strings.Split(after, "\n")
	if strings.Join(beforeRows[:10], "\n") != strings.Join(afterRows[:10], "\n") {
		t.Fatal("off-screen content changed visible layout")
	}
}

func TestAppServerStatusSelectionCopy(t *testing.T) {
	for _, action := range []byte{'c', 3} {
		t.Run(fmt.Sprintf("key=%d", action), func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.model = "gpt-6-sol"
			u.Turn = "active"
			u.testActivity(u.shell.Transcript).Apply(sessionpresentation.ActivityEvent{Kind: "entries", Entries: []sessionpresentation.ActivityEntry{{Seq: 1, Agent: "Main", Kind: "text", Text: "Hidden transcript must not be selected"}}})
			appServerTestKeys(t, u, "/status\r")
			screen := vt.NewEmulator(160, 48)
			defer screen.Close()
			if err := u.testPaint(screen, 160, 48); err != nil {
				t.Fatal(err)
			}

			defer terminalTest(u).Diff.Close()
			x, y := -1, -1
			for i, row := range strings.Split(screen.String(), "\n") {
				if before, _, ok := strings.Cut(row, "gpt-6-sol"); ok {
					x, y = ansi.StringWidth(before), i
					break
				}
			}
			if x < 0 {
				t.Fatal("status value not painted")
			}
			drag := func() {
				t.Helper()
				for _, event := range []string{fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1), fmt.Sprintf("\x1b[<32;%d;%dM", x+9, y+1), fmt.Sprintf("\x1b[<0;%d;%dm", x+9, y+1)} {
					for _, key := range []byte(event) {
						if err := terminalTest(u).key(key); err != nil {
							t.Fatal(err)
						}
					}
				}
				if terminalTest(u).Selection == nil || terminalTest(u).Selection.Text() != "gpt-6-sol" {
					t.Fatalf("wrong status selection: %+v", terminalTest(u).Selection)
				}
			}
			drag()
			if err := u.testPaint(screen, 160, 48); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(screen.String(), "copy") {
				t.Fatal("copy control missing")
			}
			if err := terminalTest(u).key(action); err != nil {
				t.Fatal(err)
			}
			want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("gpt-6-sol")) + "\x07"
			if terminalTest(u).Frame.Clipboard != want || u.statusPanel == nil || u.Turn != "active" || terminalTest(u).Selection != nil {
				t.Fatal("copy failed or dismissed/interrupted status")
			}
			if err := u.testPaint(screen, 160, 48); err != nil {
				t.Fatal(err)
			}
			drag()
			if err := terminalTest(u).send("\x1b"); err != nil {
				t.Fatal(err)
			}
			if terminalTest(u).Selection != nil || u.statusPanel == nil {
				t.Fatal("first Escape must clear selection only")
			}
			drag()
			if err := terminalTest(u).send("q"); err != nil {
				t.Fatal(err)
			}
			if u.statusPanel != nil || terminalTest(u).Selection != nil {
				t.Fatal("closing retained panel selection")
			}
			if err := u.testPaint(screen, 160, 48); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(screen.String(), "Session status") || len(u.attached.source.Entries) != 1 {
				t.Fatal("selection resurrected panel or changed transcript")
			}
		})
	}
}

// Fixtures publish source transitions before driving the actual terminal owner.
func (u *appServerUI) statusPanelFrame(width, height int) []string {
	u.settlePresentation()
	return u.shell.Main.Panel.Render(&u.shell.Transcript.Painter.Painter, width, height)
}
func (u *appServerUI) statusPanelKey(key string) bool {
	u.settlePresentation()
	if !u.shell.Main.Panel.Open() {
		return false
	}
	action, clear := u.shell.Main.Panel.Key(key)
	if clear && terminalTest(u) != nil {
		terminalTest(u).Selection = nil
	}
	u.applyPanelAction(action)
	u.dirty = true
	u.settlePresentation()
	return true
}
