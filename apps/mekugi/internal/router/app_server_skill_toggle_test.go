package router

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/modules/conversation/completion"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestComposerSkillSaveRequiresEffectiveState(t *testing.T) {
	for _, test := range []struct {
		name, result string
		late         bool
	}{
		{name: "missing", result: `{}`},
		{name: "null", result: `{"effectiveEnabled":null}`},
		{name: "late_missing", result: `{}`, late: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.session.cwd = "/work"
			u.picker.Catalog([]completion.Choice{{Name: "review", Path: "/work/review/SKILL.md", Enabled: true}}, "/work", true, "")
			appServerTestKeys(t, u, "/skills\r2 ")
			write := pickerRequests(t, wire)[0]
			if test.late {
				u.pickerKey("\x1b")
				appServerTestKeys(t, u, "@f")
				requests := pickerRequests(t, wire)
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"current file search failed"}}`, requests[len(requests)-1].ID))
			}
			before := u.picker.View()
			pickerReply(t, u, write.ID, test.result)
			if !u.picker.Skills[0].Enabled {
				t.Fatal("missing boolean confirmation disabled the skill")
			}
			if test.late {
				if !reflect.DeepEqual(u.picker.View(), before) || !u.feedback.Alert || !strings.Contains(u.feedback.Text, "Invalid skill save response") {
					t.Fatalf("late invalid confirmation lost query state or notice: picker=%+v notice=%+v", u.picker.View(), u.feedback)
				}
			} else if !strings.Contains(u.picker.Problem, "Invalid skill save response") {
				t.Fatalf("owning manager lost invalid-response error: %+v", u.picker.View())
			}
		})
	}
}

func TestComposerLateSkillSavePreservesCurrentSearch(t *testing.T) {
	for _, test := range []struct {
		name, response          string
		reopen, switchWorkspace bool
	}{
		{name: "file_success", response: `"result":{"effectiveEnabled":false}`},
		{name: "file_failure", response: `"error":{"code":-1,"message":"save denied"}`},
		{name: "file_invalid", response: `"result":{"effectiveEnabled":"invalid"}`},
		{name: "reopened_manager", reopen: true, response: `"error":{"code":-1,"message":"save denied"}`},
		{name: "reopened_manager_success", reopen: true, response: `"result":{"effectiveEnabled":false}`},
		{name: "other_workspace", reopen: true, switchWorkspace: true, response: `"result":{"effectiveEnabled":false}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.session.cwd = "/work/one"
			catalog := []completion.Choice{{Name: "review", Path: "/shared/review/SKILL.md", Enabled: true}}
			u.picker.Catalog(catalog, u.session.cwd, true, "")
			appServerTestKeys(t, u, "/skills\r2 ")
			write := pickerRequests(t, wire)[0]
			if write.Method != "skills/config/write" {
				t.Fatalf("expected skill write, got %+v", write)
			}
			u.pickerKey("\x1b")
			if test.switchWorkspace {
				u.session.cwd = "/work/two"
			}
			if test.reopen {
				appServerTestKeys(t, u, "/skills\r2")
				requests := pickerRequests(t, wire)
				load := requests[len(requests)-1]
				if load.Method != "skills/list" {
					t.Fatalf("expected catalog reload, got %+v", load)
				}
				pickerReply(t, u, load.ID, fmt.Sprintf(`{"data":[{"cwd":%q,"skills":[{"name":"review","path":"/shared/review/SKILL.md","enabled":true}],"errors":[{"message":"current catalog incomplete"}]}]}`, u.session.cwd))
			} else {
				appServerTestKeys(t, u, "@f")
				requests := pickerRequests(t, wire)
				search := requests[len(requests)-1]
				if search.Method != "fuzzyFileSearch" {
					t.Fatalf("expected file search, got %+v", search)
				}
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"current file search failed"}}`, search.ID))
			}
			before := u.picker.View()
			if test.name == "reopened_manager_success" {
				before.Choices[0].Enabled = false
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,%s}`, write.ID, test.response))
			if after := u.picker.View(); !reflect.DeepEqual(after, before) {
				t.Fatalf("late skill response changed current query: before=%+v after=%+v", before, after)
			}
			wantEnabled := test.name != "file_success" && test.name != "reopened_manager_success"
			if u.picker.Skills[0].Enabled != wantEnabled {
				t.Fatalf("confirmed write applied to wrong catalog: %+v", u.picker.Skills)
			}
			if strings.Contains(test.response, "save denied") && (!u.feedback.Alert || !strings.Contains(u.feedback.Text, "save denied")) {
				t.Fatalf("late failure lost its notice: %+v", u.feedback)
			}
		})
	}
}

func TestComposerSkillSaveDoesNotOwnReloadedCatalog(t *testing.T) {
	for _, response := range []string{`"result":{"effectiveEnabled":false}`, `"error":{"code":-1,"message":"save denied"}`} {
		t.Run(response, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.session.cwd = "/work"
			u.picker.Catalog([]completion.Choice{{Name: "review", Path: "/work/review/SKILL.md", Enabled: true}}, "/work", true, "")
			appServerTestKeys(t, u, "/skills\r2 ")
			write := pickerRequests(t, wire)[0]
			appServerTestMessage(t, u, `{"method":"skills/changed","params":{}}`)
			requests := pickerRequests(t, wire)
			load := requests[len(requests)-1]
			if load.Method != "skills/list" {
				t.Fatalf("expected invalidation reload, got %+v", load)
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"catalog reload denied"}}`, load.ID))
			before := u.picker.View()
			if !strings.Contains(before.Problem, "catalog reload denied") {
				t.Fatalf("reload error missing: %+v", before)
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,%s}`, write.ID, response))
			if after := u.picker.View(); !reflect.DeepEqual(after, before) {
				t.Fatalf("old save changed replacement catalog: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestUISnapshotSkillSavePresentationOwnership(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.session.cwd = "/work"
	u.picker.Catalog([]completion.Choice{{Name: "review", Path: "/work/review/SKILL.md", Enabled: true}}, "/work", true, "")
	appServerTestKeys(t, u, "/skills\r2 ")
	uisnapshot.Assert(t, "testdata/snapshots/skill_save_owning_manager.txt", strings.Join(u.renderPicker(72, 12), "\n")+"\n")
	appServerTestMessage(t, u, `{"method":"skills/changed","params":{}}`)
	requests := pickerRequests(t, wire)
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"catalog reload denied"}}`, requests[len(requests)-1].ID))
	uisnapshot.Assert(t, "testdata/snapshots/skill_save_replaced_catalog.txt", strings.Join(u.renderPicker(72, 12), "\n")+"\n")
	u.pickerKey("\x1b")
	appServerTestKeys(t, u, "@f")
	uisnapshot.Assert(t, "testdata/snapshots/skill_save_unrelated_file.txt", strings.Join(u.renderPicker(72, 4), "\n")+"\n")
	u.showSettingsPicker("/model", []string{"one", "two"})
	uisnapshot.Assert(t, "testdata/snapshots/skill_save_unrelated_settings.txt", strings.Join(u.renderPicker(72, 5), "\n")+"\n")
}
