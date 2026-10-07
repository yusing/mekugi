package claude

import (
	json "encoding/json/v2"
	"reflect"
	"testing"

	"github.com/yusing/mekugi/internal/session"
)

func TestClientSessionTitleFrameAndReceipt(t *testing.T) {
	client := startMockBridge(t, t.Context(), `
require('node:readline').createInterface({input:process.stdin}).on('line', text => {
  console.log(JSON.stringify({kind:'notice', text}));
  const frame = JSON.parse(text);
  console.log(JSON.stringify({...frame, failed: frame.title === 'Fail', text: frame.title === 'Fail' ? 'native save unavailable' : ''}));
});`, Config{Cwd: t.TempDir()})
	for _, name := range []string{"修復\nquoted \"title\" \\ $HOME `command` $(command)", "Fail"} {
		want := session.SessionTitle{ID: "title/request", SessionID: "native-session", Title: name}
		if err := client.RenameSession(t.Context(), want); err != nil {
			t.Fatal(err)
		}
		var frame map[string]string
		if err := json.Unmarshal([]byte(nextEvent(t, client).Text), &frame); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(frame, map[string]string{"kind": "title", "id": want.ID, "sessionID": want.SessionID, "title": want.Title}) {
			t.Fatalf("title frame = %#v", frame)
		}
		receipt := nextEvent(t, client)
		if receipt.Kind != "title" || receipt.Title == nil || *receipt.Title != want || receipt.Failed != (name == "Fail") {
			t.Fatalf("title receipt = %+v", receipt)
		}
		wantText := ""
		if name == "Fail" {
			wantText = "native save unavailable"
		}
		if receipt.Text != wantText {
			t.Fatalf("receipt error = %q, want %q", receipt.Text, wantText)
		}
	}
}
