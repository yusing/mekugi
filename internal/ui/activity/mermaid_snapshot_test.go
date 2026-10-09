package activity

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestUISnapshotMermaid(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		width        int
	}{
		{"historical_original", `flowchart LR
Agent["Codex agent"] --> Mekugi["Mekugi"]
Mekugi --> Tools["Compact, recoverable tools"]
Mekugi --> Activity["Live subagent activity"]
Mekugi -. "Codex retains authority" .-> Codex["Stock Codex"]
Codex --> Owns["Editing, execution, sandbox, permissions,<br/>command sessions, and patch review"]`, 80},
		{"historical_shorter", `flowchart LR
Agent["Codex agent"] --> Mekugi["Mekugi"]
Mekugi --> Tools["Compact tools"]
Mekugi --> Activity["Live subagent activity"]
Mekugi -. "Codex owns execution" .-> Codex["Stock Codex"]
Codex --> Editing["Editing, execution, and sandbox"]
Codex --> Permissions["Permissions and patch review"]`, 80},
		{"horizontal_multiline", `flowchart RL; A(["请求<br>Start"]) --> B{"Ready<br />now?"}`, 80},
		{"reverse_fallback", `flowchart RL; A(["请求<br>Start"]) --> B{"Ready<br />now?"}`, 20},
		{"quoted_narrow", "> ```mermaid\n> flowchart TD; A[请求处理请求处理请求处理] --> B[完成]\n> ```", 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := tc.source
			if !strings.HasPrefix(text, ">") {
				text = "```mermaid\n" + text + "\n```"
			}
			p := Painter{Theme: livediff.DarkTheme}
			uisnapshot.Assert(t, "testdata/snapshots/mermaid_"+tc.name+".txt", strings.Join(p.Markdown(text, tc.width), "\n")+"\n")
		})
	}
}
