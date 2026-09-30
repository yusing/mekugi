package router

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

// These fixtures originated from Codex TUI snapshots at 86be5320b068ef67b56348b02aa8c33706955da6:
// chatwidget/tests/popups_and_settings.rs (skills_menu_default_mentions_shortcut)
// and bottom_pane/skills_toggle_view.rs (skills_toggle_basic).
func TestUISnapshotSkillsMenuAndActions(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.view.painter.Theme = livediff.DarkTheme
	appServerTestKeys(t, u, "/skills\r")
	if u.picker.modal != "menu" || !u.picker.open || u.pickerHeight(80) != 8 {
		t.Fatalf("menu state/height = %+v, %d", u.picker, u.pickerHeight(80))
	}
	uisnapshot.Assert(t, "testdata/snapshots/skills_menu_default.txt", strings.Join(u.renderPicker(80, 8), "\n")+"\n")
	if !u.skillsModalKey("2") || u.picker.modal != "manage" {
		t.Fatalf("numeric manage action = %+v", u.picker)
	}
	u.closeSkillsModal()
	appServerTestKeys(t, u, "/skills\r")
	u.skillsModalKey("\x1b[B")
	if u.picker.selected != 1 {
		t.Fatalf("down selected = %d", u.picker.selected)
	}
	u.skillsModalKey("\x1b[A")
	if u.picker.selected != 0 {
		t.Fatalf("up selected = %d", u.picker.selected)
	}
	u.skillsModalKey("\r")
	if u.picker.open || u.draft != "$" {
		t.Fatalf("list action = open %v, draft %q", u.picker.open, u.draft)
	}
}

func TestUISnapshotSkillsManageFilteringAndSave(t *testing.T) {
	u, w := newAppServerTestUI()
	u.view.painter.Theme = livediff.DarkTheme
	u.session.cwd = "/work"
	u.picker.modal, u.picker.open = "manage", true
	u.picker.skills = []composerChoice{
		{name: "repo_scout", display: "Repo Scout", description: "Summarize the repo layout", path: "/tmp/skills/repo_scout.toml", enabled: true},
		{name: "changelog_writer", display: "Changelog Writer", description: "Draft release notes", path: "/tmp/skills/changelog_writer.toml"},
	}
	u.picker.skillsLoaded = true
	u.picker.skillsCwd = "/work"
	u.rememberSkillState()
	u.filterSkills("")
	if len(u.picker.choices) != 2 || u.pickerHeight(72) != 12 {
		t.Fatalf("management rows/height = %d, %d", len(u.picker.choices), u.pickerHeight(72))
	}
	uisnapshot.Assert(t, "testdata/snapshots/skills_toggle_basic.txt", strings.Join(u.renderPicker(72, 12), "\n")+"\n")
	appServerTestKeys(t, u, "changelog")
	if len(u.picker.choices) != 1 || u.picker.choices[0].name != "changelog_writer" {
		t.Fatalf("disabled skill missing from management filter: %+v", u.picker.choices)
	}
	u.picker.query = ""
	u.filterSkills("")
	u.picker.selected = 1
	u.skillsModalKey(" ")
	if !strings.Contains(w.String(), `"method":"skills/config/write"`) || !strings.Contains(w.String(), `"path":"/tmp/skills/changelog_writer.toml"`) || !strings.Contains(w.String(), `"enabled":true`) {
		t.Fatalf("save request = %q", w.String())
	}
	requests := pickerRequests(t, w)
	pickerReply(t, u, requests[len(requests)-1].ID, `{"effectiveEnabled":true}`)
	if !u.picker.skills[1].enabled || u.picker.problem != "" {
		t.Fatalf("effectiveEnabled not applied: %+v", u.picker)
	}
	u.skillsModalKey(" ")
	requests = pickerRequests(t, w)
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"denied"}}`, requests[len(requests)-1].ID))
	if !u.picker.skills[1].enabled || !strings.Contains(u.picker.problem, "denied") {
		t.Fatalf("failed save changed state or lost error: %+v", u.picker)
	}
}

func TestUISnapshotSkillsManagerViewportAndPaste(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.view.painter.Theme = livediff.DarkTheme
	u.session.cwd = "/work"
	u.picker.modal, u.picker.open = "manage", true
	u.picker.skillsLoaded, u.picker.skillsCwd = true, "/work"
	for i := range 15 {
		u.picker.skills = append(u.picker.skills, composerChoice{name: fmt.Sprintf("skill-%02d", i), path: fmt.Sprintf("/work/%d/SKILL.md", i), enabled: true})
	}
	u.refreshPicker()
	u.picker.selected = 14
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
	if u.draft != "" || u.picker.query != "skill-03" || len(u.picker.choices) != 1 {
		t.Fatalf("paste escaped manager: %q %+v", u.draft, u.picker)
	}
}

func TestSkillsShellVariablesStayLiteral(t *testing.T) {
	for _, query := range []string{"HOME", "PATH", "1", "?", "-", "_", "{HOME}"} {
		u, _ := newAppServerTestUI()
		u.draft = "$" + query
		if u.completionTarget().kind != 0 {
			t.Fatalf("shell token $%s opened picker", query)
		}
	}
}

func TestUISnapshotSkillsColumnsMeasureOnlyVisibleRows(t *testing.T) {
	for _, modal := range []string{"", "manage"} {
		t.Run("modal="+modal, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.view.painter.Theme = livediff.DarkTheme
			u.picker.modal, u.picker.target.kind = modal, '$'
			for i := range 9 {
				u.picker.choices = append(u.picker.choices, composerChoice{
					name: fmt.Sprintf("row%d", i), description: "description", enabled: true,
				})
			}
			height := u.pickerHeight(48)
			before := strings.Join(u.renderPicker(48, height), "\n")
			name := "completion"
			if modal != "" {
				name = modal
			}
			uisnapshot.Assert(t, "testdata/snapshots/skills_columns_"+name+".txt", before+"\n")
			u.picker.choices[8].display = "an-extremely-long-name-outside-the-visible-viewport"
			if after := strings.Join(u.renderPicker(48, height), "\n"); after != before {
				t.Fatalf("off-screen name changed visible alignment:\nbefore:\n%s\nafter:\n%s", ansi.Strip(before), ansi.Strip(after))
			}
			u.picker.selected = 8
			u.renderPicker(48, height)
			u.picker.selected = 0
			if after := strings.Join(u.renderPicker(48, height), "\n"); after != before {
				t.Fatal("scrolling back retained off-screen column width")
			}
		})
	}
}
