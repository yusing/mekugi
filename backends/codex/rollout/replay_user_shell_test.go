package rollout

import (
	"encoding/json/jsontext"
	"fmt"
	"testing"
)

func TestReplayUserShellOrigin(t *testing.T) {
	for _, source := range []string{"user_shell", "agent", "unified_exec_startup", ""} {
		raw := jsontext.Value(fmt.Sprintf(`{"type":"CommandExecution","id":"command","source":%q,"command":["bash","-lc","cat notes.txt"],"aggregated_output":"retained output\n","exit_code":0}`, source))
		item, supported, err := replayItem(raw)
		if err != nil || !supported || item.UserShell != (source == "user_shell") || item.Command != "cat notes.txt" || item.AggregatedOutput == nil || *item.AggregatedOutput != "retained output\n" {
			t.Fatalf("source %q: item=%+v supported=%v err=%v", source, item, supported, err)
		}
	}
}
