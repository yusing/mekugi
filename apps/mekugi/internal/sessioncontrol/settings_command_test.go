package sessioncontrol_test

import (
	"reflect"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
)

func TestPermissionArguments(t *testing.T) {
	for _, test := range []struct {
		args string
		want map[string]any
	}{
		{":approve-for-me", map[string]any{"permissions": ":workspace", "approvalPolicy": "on-request", "approvalsReviewer": "auto_review"}},
		{`:danger-full-access --approval-policy=on-request`, map[string]any{"permissions": ":danger-full-access", "approvalPolicy": "on-request", "approvalsReviewer": "user"}},
		{`'team profile' --approval-policy '{"granular":{"sandbox_approval":true,"rules":true,"mcp_elicitations":true}}'`, map[string]any{"permissions": "team profile", "approvalPolicy": map[string]any{"granular": map[string]any{"sandbox_approval": true, "rules": true, "mcp_elicitations": true, "skill_approval": false, "request_permissions": false}}}},
		{`"literal$profile"`, map[string]any{"permissions": "literal$profile"}},
	} {
		got, err := sessioncontrol.ParsePermissionCommand(test.args)
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Fatalf("%q: got %#v err=%v", test.args, got, err)
		}
	}
	for _, args := range []string{"", "--reviewer user", "profile --reviewer unknown", "profile --approval-policy unknown", "profile --approval-policy=null", "profile --approval-policy='{}'", "profile --approval-policy='{\"granular\":{\"sandbox_approval\":null}}'", "profile --reviewer user --reviewer auto_review", `"unfinished`} {
		if _, err := sessioncontrol.ParsePermissionCommand(args); err == nil {
			t.Fatalf("accepted invalid permission arguments %q", args)
		}
	}
}
