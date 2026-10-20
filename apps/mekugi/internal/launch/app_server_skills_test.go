package launch

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionpresentation"

	"github.com/yusing/mekugi/modules/conversation/completion"

	"github.com/yusing/mekugi/libraries/diffrender"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

// These fixtures originated from Codex TUI snapshots at 86be5320b068ef67b56348b02aa8c33706955da6:
// chatwidget/tests/popups_and_settings.rs (skills_menu_default_mentions_shortcut)
// and bottom_pane/skills_toggle_view.rs (skills_toggle_basic).
func TestUISnapshotSkillsMenuAndActions(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	appServerTestKeys(t, u, "/skills\r")
	if u.picker.Kind != "menu" || !u.picker.Open || u.pickerHeight(80) != 8 {
		t.Fatalf("menu state/height = %+v, %d", u.picker, u.pickerHeight(80))
	}
	uisnapshot.Assert(t, "testdata/snapshots/skills_menu_default.txt", strings.Join(u.renderPicker(80, 8), "\n")+"\n")
	if !u.pickerKey("2") || u.picker.Kind != "manage" {
		t.Fatalf("numeric manage action = %+v", u.picker)
	}
	u.picker.closeSkillsModal(&u.attached, u.shell, u.clock, u.Editor)
	appServerTestKeys(t, u, "/skills\r")
	u.pickerKey("\x1b[B")
	settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	if u.shell.Main.Menu.Selected != 1 {
		t.Fatalf("down selected = %d", u.shell.Main.Menu.Selected)
	}
	u.pickerKey("\x1b[A")
	settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	if u.shell.Main.Menu.Selected != 0 {
		t.Fatalf("up selected = %d", u.shell.Main.Menu.Selected)
	}
	appServerTestKeys(t, u, "\r")
	if !u.picker.Open || u.picker.Kind != "" || u.picker.Target.Kind != '$' || u.Text != "$" {
		t.Fatalf("list action = open %v, draft %q", u.picker.Open, u.Text)
	}
}

func TestUISnapshotSkillsManageFilteringAndSave(t *testing.T) {
	u, w := newAppServerTestUI()
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	u.attached.cwd = "/work"
	u.picker.Kind, u.picker.Open = "manage", true
	u.picker.Skills = []completion.Choice{
		{Name: "repo_scout", Display: "Repo Scout", Description: "Summarize the repo layout", Path: "/tmp/skills/repo_scout.toml", Enabled: true},
		{Name: "changelog_writer", Display: "Changelog Writer", Description: "Draft release notes", Path: "/tmp/skills/changelog_writer.toml"},
	}
	u.picker.SkillsLoaded = true
	u.picker.SkillsCwd = "/work"
	u.picker.RememberSkills()
	u.picker.FilterSkills("", u.picker.Kind == "manage")
	if len(u.picker.Choices) != 2 || u.pickerHeight(72) != 12 {
		t.Fatalf("management rows/height = %d, %d", len(u.picker.Choices), u.pickerHeight(72))
	}
	uisnapshot.Assert(t, "testdata/snapshots/skills_toggle_basic.txt", strings.Join(u.renderPicker(72, 12), "\n")+"\n")
	appServerTestKeys(t, u, "changelog")
	if len(u.picker.Choices) != 1 || u.picker.Choices[0].Name != "changelog_writer" {
		t.Fatalf("disabled skill missing from management filter: %+v", u.picker.Choices)
	}
	u.picker.Query = ""
	u.picker.FilterSkills("", u.picker.Kind == "manage")
	settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	u.shell.Main.Menu.Selected = 1
	u.pickerKey(" ")
	if !strings.Contains(w.String(), `"method":"skills/config/write"`) || !strings.Contains(w.String(), `"path":"/tmp/skills/changelog_writer.toml"`) || !strings.Contains(w.String(), `"enabled":true`) {
		t.Fatalf("save request = %q", w.String())
	}
	requests := pickerRequests(t, w)
	pickerReply(t, u, requests[len(requests)-1].ID, `{"effectiveEnabled":true}`)
	if !u.picker.Skills[1].Enabled || u.picker.Problem != "" {
		t.Fatalf("effectiveEnabled not applied: %+v", u.picker)
	}
	u.pickerKey(" ")
	requests = pickerRequests(t, w)
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"denied"}}`, requests[len(requests)-1].ID))
	if !u.picker.Skills[1].Enabled || !strings.Contains(u.picker.Problem, "denied") {
		t.Fatalf("failed save changed state or lost error: %+v", u.picker)
	}
}

func TestUISnapshotSkillsManagerViewportAndPaste(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	u.attached.cwd = "/work"
	u.picker.Kind, u.picker.Open = "manage", true
	u.picker.SkillsLoaded, u.picker.SkillsCwd = true, "/work"
	for i := range 15 {
		u.picker.Skills = append(u.picker.Skills, completion.Choice{Name: fmt.Sprintf("skill-%02d", i), Path: fmt.Sprintf("/work/%d/SKILL.md", i), Enabled: true})
	}
	u.picker.refreshPicker(&u.attached, u.shell, u.clock, u.Requests, u.Editor, u.ctx, u.skillEnvironment)
	settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
	u.shell.Main.Menu.Selected = 14
	rows := u.renderPicker(60, u.pickerHeight(60))
	t.Run("viewport", func(t *testing.T) {
		uisnapshot.Assert(t, "testdata/snapshots/skills_manager_viewport.txt", strings.Join(rows, "\n")+"\n")
	})
	for _, height := range []int{1, 2, 4} {
		t.Run(fmt.Sprintf("height_%d", height), func(t *testing.T) {
			uisnapshot.Assert(t, fmt.Sprintf("testdata/snapshots/skills_manager_height_%d.txt", height), strings.Join(u.renderPicker(30, height), "\n")+"\n")
		})
	}
	appServerTestKeys(t, u, "\x1b[200~skill-03\x1b[201~")
	if u.Text != "" || u.picker.Query != "skill-03" || len(u.picker.Choices) != 1 {
		t.Fatalf("paste escaped manager: %q %+v", u.Text, u.picker)
	}
}

func TestSkillsShellVariablesStayLiteral(t *testing.T) {
	for _, query := range []string{"HOME", "PATH", "1", "?", "-", "_", "{HOME}"} {
		u, _ := newAppServerTestUI()
		u.Text = "$" + query
		if u.picker.completionTarget(u.Draft, u.attached.conversation.ShellMode()).Kind != 0 {
			t.Fatalf("shell token $%s opened picker", query)
		}
	}
}

func TestUISnapshotSkillsColumnsMeasureOnlyVisibleRows(t *testing.T) {
	for _, modal := range []string{"", "manage"} {
		t.Run("modal="+modal, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
			u.picker.Kind, u.picker.Target.Kind, u.picker.Open = sessionpresentation.MenuKind(modal), '$', true
			for i := range 9 {
				u.picker.Choices = append(u.picker.Choices, completion.Choice{
					Name: fmt.Sprintf("row%d", i), Description: "description", Enabled: true,
				})
			}
			height := u.pickerHeight(48)
			before := strings.Join(u.renderPicker(48, height), "\n")
			name := "completion"
			if modal != "" {
				name = modal
			}
			uisnapshot.Assert(t, "testdata/snapshots/skills_columns_"+name+".txt", before+"\n")
			u.picker.Choices[8].Display = "an-extremely-long-name-outside-the-visible-viewport"
			if after := strings.Join(u.renderPicker(48, height), "\n"); after != before {
				t.Fatalf("off-screen name changed visible alignment:\nbefore:\n%s\nafter:\n%s", ansi.Strip(before), ansi.Strip(after))
			}
			settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
			u.shell.Main.Menu.Selected = 8
			u.renderPicker(48, height)
			settlePresentation(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.childHistoryState, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, u.serviceTiers, u.title, u.backendVersion, u.restoring != nil, u.side, u.clock, &u.questions, &u.approvals, u.statusPanel != nil || u.resumePicker != nil)
			u.shell.Main.Menu.Selected = 0
			if after := strings.Join(u.renderPicker(48, height), "\n"); after != before {
				t.Fatal("scrolling back retained off-screen column width")
			}
		})
	}
}
