package claude

import (
	"reflect"
	"testing"

	"github.com/yusing/mekugi/internal/session"
)

func TestClaudeSessionSwitchWorkspaceReceipt(t *testing.T) {
	client := startMockBridge(t, t.Context(), `
const readline = require('node:readline');
readline.createInterface({input: process.stdin}).on('line', line => {
  const frame = JSON.parse(line);
  console.log(JSON.stringify({...frame, title: 'Saved native title'}));
});`, Config{Cwd: t.TempDir()})
	want := session.SessionChange{ID: "switch/request", SessionID: "native-session", Cwd: "/workspace with spaces", Title: "Saved native title"}
	if err := client.ChangeSession(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	event := nextEvent(t, client)
	if event.Kind != "session_change" || event.Change == nil || !reflect.DeepEqual(*event.Change, want) {
		t.Fatalf("workspace receipt = %+v", event)
	}
}
