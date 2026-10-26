package launch

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func TestAppServerPermissionsInlineConfirmation(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.Turn = "active"
	appServerTestKeys(t, u, "/permissions :read-only --reviewer=user\r")
	assertAppServerSettingsRequest(t, wire, "thread/settings/update", map[string]any{"threadId": "main", "permissions": ":read-only", "approvalPolicy": "on-request", "approvalsReviewer": "user"})
	appServerTestKeys(t, u, "waiting prompt\r")
	if wire.Len() != 0 || !u.Settings.Pending() || len(u.attached.conversation.Unsent) != 1 {
		t.Fatal("permission update did not hold dependent input")
	}
	appServerTestMessage(t, u, `{"id":1,"result":{}}`)
	if !u.Settings.Pending() {
		t.Fatal("acknowledgement released input before permission confirmation")
	}
	appServerTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"main","threadSettings":{"model":"test","effort":"high","activePermissionProfile":{"id":":read-only"},"approvalPolicy":"on-request","approvalsReviewer":"user"}}}`)
	if u.Settings.Pending() || u.Settings.Permissions.Active == nil || u.Settings.Permissions.Active.ID != ":read-only" || !strings.Contains(u.attached.feedback.Text, "next turn") {
		t.Fatalf("permission confirmation not applied: %+v notice=%q", u.Settings, u.attached.feedback.Text)
	}
	if strings.Contains(wire.String(), "turn/settings/update") {
		t.Fatal("profile was sent to unsupported active-turn API")
	}
	wire.Reset()
	appServerTestKeys(t, u, "/permissions :read-only --reviewer=user\r")
	if wire.Len() != 0 || u.Settings.Pending() || u.Text != "" {
		t.Fatal("unchanged permissions waited for a suppressed host notification")
	}
}

func TestAppServerPermissionsPickerLifecycle(t *testing.T) {
	for _, action := range []string{"cancel", "select", "denied", "failed discovery"} {
		t.Run(action, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			appServerTestKeys(t, u, "/permissions\r")
			if strings.Contains(wire.String(), `"cursor"`) {
				t.Fatal("initial discovery sent an invalid empty cursor")
			}
			request := btwTestRequest(t, wire, "permissionProfile/list", "")
			if !u.Settings.BlocksInput() || !u.picker.Loading || u.Text != "" {
				t.Fatal("permission discovery did not gate input or clear command")
			}
			if action == "failed discovery" {
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"discovery failed"}}`, request.ID))
				if u.picker.Loading || !strings.Contains(u.picker.Problem, "discovery failed") {
					t.Fatal("failed discovery left a spinner or hid failure")
				}
			} else {
				btwTestReply(t, u, request, `{"data":[{"id":":read-only","description":"Read workspace files","allowed":true},{"id":"restricted","allowed":false}],"nextCursor":null}`)
				if u.picker.Loading || len(u.picker.Choices) != 2 {
					t.Fatalf("profile choices = %+v", u.picker.Choices)
				}
			}
			switch action {
			case "select":
				appServerTestKeys(t, u, "\r")
				assertAppServerSettingsRequest(t, wire, "thread/settings/update", map[string]any{"threadId": "main", "permissions": ":read-only", "approvalPolicy": "on-request", "approvalsReviewer": "user"})
				if u.picker.Open || !u.Settings.Pending() {
					t.Fatal("picker did not transfer its input gate to the host update")
				}
			case "denied":
				appServerTestKeys(t, u, "\x1b[B\r")
				if wire.Len() != 0 || !u.picker.Open || u.picker.Problem == "" {
					t.Fatal("unavailable profile was applied or rejection hidden")
				}
				fallthrough
			default:
				if _, err := u.input(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x1b"}); err != nil {
					t.Fatal(err)
				}
				if u.Settings.Pending() || u.picker.Open {
					t.Fatal("dismissal leaked the selection input gate")
				}
			}
		})
	}
}

func TestAppServerPermissionsRejectedRestoresWaiting(t *testing.T) {
	u, wire := newAppServerTestUI()
	appServerTestKeys(t, u, "/permissions custom --approval-policy=never\r")
	request := btwTestRequest(t, wire, "thread/settings/update", "main")
	appServerTestKeys(t, u, "do work under new policy\r")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"profile forbidden"}}`, request.ID))
	if wire.Len() != 0 || u.Settings.Pending() || u.Text != "do work under new policy" || !u.attached.feedback.Alert || !strings.Contains(u.attached.feedback.Text, "Permissions unchanged") {
		t.Fatalf("rejected permissions sent or lost dependent input: draft=%q wire=%s notice=%q", u.Text, wire.String(), u.attached.feedback.Text)
	}
}

func TestAppServerPermissionsEquivalentPolicy(t *testing.T) {
	u, wire := newAppServerTestUI()
	appServerTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"main","threadSettings":{"model":"test","activePermissionProfile":{"id":":read-only"},"approvalPolicy":{"granular":{"sandbox_approval":true,"rules":true,"mcp_elicitations":true,"skill_approval":false,"request_permissions":false}},"approvalsReviewer":"user"}}}`)
	appServerTestKeys(t, u, `/permissions :read-only --approval-policy='{"granular":{"sandbox_approval":true,"rules":true,"mcp_elicitations":true}}'`+"\r")
	if wire.Len() != 0 || u.Settings.Pending() || u.Text != "" {
		t.Fatal("equivalent granular policy waited for a suppressed notification")
	}
	command := "/permissions :read-only --approval-policy=null"
	appServerTestKeys(t, u, command+"\r")
	if wire.Len() != 0 || u.Settings.Pending() || u.Text != command || !u.attached.feedback.Alert {
		t.Fatal("null approval policy created an unconfirmable update")
	}
}

func TestUISnapshotPermissionPicker(t *testing.T) {
	for _, width := range []int{40, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.clock = func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) }
			u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
			appServerTestKeys(t, u, "/permissions\r")
			request := btwTestRequest(t, wire, "permissionProfile/list", "")
			btwTestReply(t, u, request, `{"data":[{"id":":read-only","description":"Read workspace files; ask before edits","allowed":true},{"id":":workspace","description":"Edit workspace files; ask for outside access","allowed":true},{"id":":danger-full-access","description":"Unrestricted filesystem and network access","allowed":false}]}`)
			assertNativeUISnapshot(t, fmt.Sprintf("native-permissions-%d", width), u.renderPicker(width, 8))
		})
	}
}

func TestNativePermissionSelectionRetiresWithView(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	n := newNativeViews(t.Context(), sessionapi.NewLive().Native(), u.presentationState, u.terminalViews, u.activity, u.Requests)
	defer n.close()
	if refusal := n.attach(sessionapi.NativeRequest{Attach: true, Attachment: "view", Thread: "main", Workspace: "/workspace", Context: t.Context(), ViewContext: t.Context()}, &u.attached, u.Thread); refusal != "" {
		t.Fatal(refusal)
	}
	v := n.views["view"]
	publishSessionView(&v.attached, &u.Client, &v.picker, "", preferences.Applied{}, u.serviceTiers, "", "", false, nil, time.Now(), true)
	editor := new(composer.Editor)
	editor.Text = "/permissions"
	if handled, err := settingsCommand(&v.attached, v.shell, &u.Client, &v.picker, &editor, preferences.Applied{}, &u.serviceTiers, "", "", false, nil, u.clock, &u.questions, &u.approvals, false, u.ownedImages, u.panes, nil, false, editor.Text); !handled || err != nil {
		t.Fatalf("permission selection handled=%v err=%v", handled, err)
	}
	request := btwTestRequest(t, wire, "permissionProfile/list", "")
	if !u.Settings.BlocksInput() {
		t.Fatal("native selection failed to gate execution input")
	}
	n.retire("view")
	if u.Settings.Pending() || u.Settings.BlocksInput() {
		t.Fatal("retired view retained the permission gate")
	}
	btwTestReply(t, u, request, `{"data":[{"id":":workspace","allowed":true}]}`)
	if u.picker.Open || wire.Len() != 0 {
		t.Fatal("retired discovery mutated the execution view")
	}
}
