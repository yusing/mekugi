package router

import (
	"encoding/base64"
	"encoding/json/jsontext"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/livediff"
)

func TestAppServerStatusLocalAndAccountRefresh(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.session.start("main", "/workspace")
	u.agents = newLiveActivityView()
	u.model, u.reasoningEffort, u.serviceTier = "gpt-6-sol", "high", "priority"
	u.turn, u.status = "active", "Working"
	u.statusConfig.Provider = "openai"
	u.statusConfig.Approval = jsontext.Value(`"never"`)
	u.statusConfig.Sandbox.Type = "danger-full-access"
	u.statusConfig.Instructions = []string{"/workspace/AGENTS.md"}
	appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{"threadId": "main", "tokenUsage": map[string]any{"total": map[string]any{"inputTokens": 1200, "outputTokens": 30}, "last": map[string]any{"totalTokens": 1000}, "modelContextWindow": 4000}})
	appServerTestKeys(t, u, "/status\r")
	if len(u.view.entries) != 0 || u.draft != "" || u.turn != "active" || u.status != "Working" {
		t.Fatalf("status altered session: draft=%q turn=%q status=%q entries=%d", u.draft, u.turn, u.status, len(u.view.entries))
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
	if strings.Contains(body, "Token usage:") || strings.Contains(body, "Loading") || len(u.view.entries) != 0 || len(u.statusReports) != 0 {
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
	if len(u.view.entries) != 0 {
		t.Fatalf("cards=%d", len(u.view.entries))
	}
	first, second := statusReportText(firstPanel), statusReportText(u.statusPanel)
	if !strings.Contains(first, "Model: first") || strings.Contains(first, "offline") || strings.Contains(first, "API key") || !strings.Contains(second, "Model: second") || !strings.Contains(second, "API key") {
		t.Fatalf("crossed replies: %q / %q", first, second)
	}
	if u.alert || u.status != "" || len(u.requests) != 0 || len(u.statusReports) != 0 {
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
	u.session.start("main", "/old")
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
		u.session.start("main", "/work")
		u.session.agent("/root").InputTokens = 12
		appServerTestKeys(t, u, "/status\r")
		appServerTestMessage(t, u, `{"id":1,"result":`+response+`}`)
		if len(u.statusReports) != 0 || strings.Contains(statusReportText(u.statusPanel), "Loading") || strings.Contains(wire.String(), "rateLimits") {
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
			u.turn, u.status = "active", "Working"
			u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "Main", Kind: "text", Text: "Existing transcript"}}})
			shell := &terminalUI{main: u, layout: terminalLayout{codex: terminalRect{0, 0, 60, 12}}}
			appServerTestKeys(t, u, "/status\r")
			u.statusPanel.body = nil
			for range 40 {
				u.statusPanel.body = append(u.statusPanel.body, statusField{group: "Session", value: "long status row"})
			}
			u.renderStatus(u.statusPanel, statusNotice("Loading account…"))
			for _, width := range []int{1, 12, 60} {
				frame, _ := u.mainFrame(width, 8, 0)
				for _, row := range frame {
					if ansi.StringWidth(row) > width {
						t.Fatalf("overflow at %d: %q", width, row)
					}
				}
				if u.mainContentPainted {
					t.Fatal("covered transcript acknowledged")
				}
			}
			if err := shell.send("\x1b[6~"); err != nil {
				t.Fatal(err)
			}
			if u.statusPanel.top == 0 {
				t.Fatal("panel did not scroll")
			}
			before := u.statusPanel.top
			if err := shell.mouse("\x1b[<65;2;3M"); err != nil {
				t.Fatal(err)
			}
			if u.statusPanel.top != before+1 {
				t.Fatal("wheel did not scroll panel")
			}
			if err := shell.send(closeKey); err != nil {
				t.Fatal(err)
			}
			if u.statusPanel != nil || u.turn != "active" || u.status != "Working" || len(u.view.entries) != 1 || u.view.entries[0].Text != "Existing transcript" {
				t.Fatal("closing changed turn or transcript")
			}
			// A response to a closed panel must not fetch limits or resurrect UI.
			appServerTestMessage(t, u, `{"id":1,"result":{"account":{"type":"chatgpt","planType":"plus"}}}`)
			if u.statusPanel != nil || len(u.statusReports) != 0 || strings.Contains(wire.String(), "rateLimits") || strings.Contains(wire.String(), "turn/") {
				t.Fatalf("closed panel performed work: %s", wire.String())
			}
		})
	}
}

func TestAppServerStatusRefreshFailureStaysInPanel(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.status = "Working"
	appServerTestKeys(t, u, "/status\r")
	appServerTestMessage(t, u, `{"id":1,"error":{"code":-1,"message":"offline"}}`)
	if !strings.Contains(statusReportText(u.statusPanel), "offline") || u.alert || u.status != "Working" || len(u.view.entries) != 0 {
		t.Fatal("optional failure altered session")
	}
}

func TestAppServerStatusTerminalPasteCanClose(t *testing.T) {
	u, _ := newAppServerTestUI()
	shell := &terminalUI{main: u}
	for _, key := range []byte("/status\r\x1b[200~q\r\x03\x1b[B\x1b[201~") {
		if err := shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if u.statusPanel == nil || u.draft != "" || shell.paste {
		t.Fatal("paste affected panel or composer")
	}
	if err := shell.key('q'); err != nil {
		t.Fatal(err)
	}
	if u.statusPanel != nil || u.quitRequested || len(u.view.entries) != 0 {
		t.Fatal("panel trapped input after paste")
	}
}

func statusReportText(p *appServerStatusReport) string {
	var rows []string
	for _, f := range p.fields {
		text := f.value
		if f.label != "" {
			text = f.label + ": " + text
		}
		rows = append(rows, text)
	}
	return strings.Join(rows, "\n")
}

func TestAppServerStatusPanelVisual(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.model, u.reasoningEffort, u.serviceTier = "gpt-6-sol", "high", "priority"
	u.session.start("main", "/home/yusing/projects/mekugi")
	u.statusConfig.Provider = "openai"
	u.statusConfig.Approval = jsontext.Value(`"never"`)
	u.statusConfig.Sandbox.Type = "danger-full-access"
	u.statusConfig.Instructions = []string{"/home/yusing/projects/mekugi/AGENTS.md"}
	root := u.session.agent("/root")
	root.ContextKnown, root.ContextTokens, root.ContextWindow = true, 120000, 400000
	appServerTestKeys(t, u, "/status\r")
	appServerTestMessage(t, u, `{"id":1,"result":{"account":{"type":"chatgpt","email":"yusing@example.com","planType":"pro"}}}`)
	appServerTestMessage(t, u, `{"id":2,"result":{"rateLimits":{"primary":{"usedPercent":22,"windowDurationMins":300},"secondary":{"usedPercent":83,"windowDurationMins":10080},"credits":{"unlimited":false,"balance":"12.50"}}}}`)
	for _, theme := range []livediff.Theme{livediff.DarkTheme, livediff.LightTheme} {
		u.view.painter.Theme = theme
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
	u.view.painter.Theme = livediff.DarkTheme
	u.statusPanel.top = 0
	screen := vt.NewEmulator(160, 48)
	defer screen.Close()
	if err := u.paint(screen, 160, 48); err != nil {
		t.Fatal(err)
	}
	defer u.shell.diff.close()
	defer u.shell.diffScreen.Close()
	rendered := screen.String()
	for _, want := range []string{"Session status", "gpt-6-sol", "78% left", "17% left", "━━", "Esc close"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("compositor lost %q:\n%s", want, rendered)
		}
	}
	r := u.shell.layout.codex
	var panel []string
	for _, row := range strings.Split(rendered, "\n")[r.y : r.y+r.h] {
		panel = append(panel, ansi.Cut(row, r.x, r.x+r.w))
	}
	t.Log("Rendered Main pane:\n" + strings.Join(panel, "\n"))
	saved := u.statusPanel
	if err := u.shell.send("\x1b"); err != nil {
		t.Fatal(err)
	}
	if err := u.paint(screen, 160, 48); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(screen.String(), "Session status") || strings.Contains(screen.String(), "78% left") {
		t.Fatal("closed panel left painted residue")
	}
	u.statusPanel = saved
	// A long off-screen label cannot change the visible value alignment.
	u.statusPanel.top = 0
	before := strings.Join(u.statusPanelFrame(72, 12), "\n")
	u.statusPanel.fields = append(u.statusPanel.fields, statusField{group: "Permissions", label: "Offscreen", value: "not visible"})
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
			u.turn = "active"
			u.view.apply(activityPaneEvent{Kind: "entries", Entries: []activityPaneEntry{{Seq: 1, Agent: "Main", Kind: "text", Text: "Hidden transcript must not be selected"}}})
			appServerTestKeys(t, u, "/status\r")
			screen := vt.NewEmulator(160, 48)
			defer screen.Close()
			if err := u.paint(screen, 160, 48); err != nil {
				t.Fatal(err)
			}
			defer u.shell.diff.close()
			defer u.shell.diffScreen.Close()
			x, y := -1, -1
			for i, row := range strings.Split(screen.String(), "\n") {
				if at := strings.Index(row, "gpt-6-sol"); at >= 0 {
					x, y = ansi.StringWidth(row[:at]), i
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
						if err := u.shell.key(key); err != nil {
							t.Fatal(err)
						}
					}
				}
				if u.shell.selection == nil || u.shell.selection.text() != "gpt-6-sol" {
					t.Fatalf("wrong status selection: %+v", u.shell.selection)
				}
			}
			drag()
			if err := u.paint(screen, 160, 48); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(screen.String(), "copy") {
				t.Fatal("copy control missing")
			}
			if err := u.shell.key(action); err != nil {
				t.Fatal(err)
			}
			want := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("gpt-6-sol")) + "\x07"
			if u.shell.clipboard != want || u.statusPanel == nil || u.turn != "active" || u.shell.selection != nil {
				t.Fatal("copy failed or dismissed/interrupted status")
			}
			if err := u.paint(screen, 160, 48); err != nil {
				t.Fatal(err)
			}
			drag()
			if err := u.shell.send("\x1b"); err != nil {
				t.Fatal(err)
			}
			if u.shell.selection != nil || u.statusPanel == nil {
				t.Fatal("first Escape must clear selection only")
			}
			drag()
			if err := u.shell.send("q"); err != nil {
				t.Fatal(err)
			}
			if u.statusPanel != nil || u.shell.selection != nil {
				t.Fatal("closing retained panel selection")
			}
			if err := u.paint(screen, 160, 48); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(screen.String(), "Session status") || len(u.view.entries) != 1 {
				t.Fatal("selection resurrected panel or changed transcript")
			}
		})
	}
}
