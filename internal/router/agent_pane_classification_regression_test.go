package router

import (
	"strings"
	"testing"
)

func TestAgentPaneGlobAndToolReadClassification(t *testing.T) {
	const source = "/home/yusing/go/pkg/mod/github.com/charmbracelet/x/vt@*/callbacks.go"
	for _, tc := range []struct {
		name, command, want string
	}{
		{
			name:    "cat glob",
			command: "cat " + source,
			want:    "Read `" + source + "`",
		},
		{
			name:    "sed range over glob",
			command: "sed -n '230,280p' " + source,
			want:    "Read `" + source + " 230:280`",
		},
		{
			name:    "cat glob piped to head",
			command: "cat " + source + " | head -120",
			want:    "Read `" + source + "`",
		},
		{
			name:    "numbered range over glob",
			command: "nl -ba " + source + " | sed -n '1,2p'",
			want:    "Read `" + source + " 1:2`",
		},
		{
			name:    "standalone numbered glob",
			command: "nl -ba " + source,
			want:    "Read `" + source + "`",
		},
		{
			name:    "mcat glob",
			command: "mcat " + source,
			want:    "Read `" + source + "`",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolActivityShell(tc.command); got != tc.want {
				t.Fatalf("toolActivityShell(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

func TestAgentPaneInspectFileOptionsAndOperands(t *testing.T) {
	command := "inspect_file --json --max-tokens 500 a.go b.go"
	want := "Inspect `a.go`\n\nInspect `b.go`"
	if got := toolActivityShell(command); got != want {
		t.Fatalf("toolActivityShell(%q) = %q, want %q", command, got, want)
	}
}

func TestAgentPaneSkillReadAndSkillCommandClassification(t *testing.T) {
	command := "skills-mgr get golang-best-practices && skills-mgr run use-modern-go/scripts/run-tool.sh list --go-version 1.27"
	want := "Skill Read `golang-best-practices`\n\nRun `skills-mgr run use-modern-go/scripts/run-tool.sh list --go-version 1.27`"
	if got := toolActivityShell(command); got != want {
		t.Fatalf("toolActivityShell(%q) = %q, want %q", command, got, want)
	}
}

func TestAgentPaneUnsafeReadLikeCommandsRemainRun(t *testing.T) {
	for _, command := range []string{
		`cat "$(printf callbacks.go)"`,
		"cat /home/yusing/go/pkg/mod/github.com/charmbracelet/x/vt@*/callbacks.go | head -120 > callbacks.copy",
		"nl -ba /home/yusing/go/pkg/mod/github.com/charmbracelet/x/vt@*/callbacks.go | sed -n '1,2p' | head -n 1",
		"sed -n '230,280p' /home/yusing/go/pkg/mod/github.com/charmbracelet/x/vt@*/callbacks.go > callbacks.copy",
	} {
		want := "Run\n" + toolActivityFenced("bash", command)
		if got := toolActivityShell(command); got != want {
			t.Errorf("unsafe read-like command %q classified as %q, want %q", command, got, want)
		}
	}
}

func TestAgentPaneRetainsUnknownJavaScriptToolParagraph(t *testing.T) {
	blocks := parseLiveActivity(activityPaneEntry{
		Kind: "tool",
		Text: "Read `a.go`\n\nRun JavaScript · other code\n\nInspect `b.go`",
	})
	if len(blocks) != 3 || blocks[0].verb != "Read" || blocks[1].verb != "Run JavaScript" || blocks[2].verb != "Inspect" {
		t.Fatalf("genuinely unknown JavaScript was hidden: %+v", blocks)
	}

	const authored = "A note about Run JavaScript · other code should remain visible."
	text := parseLiveActivity(activityPaneEntry{Kind: "commentary", Text: authored})
	if len(text) != 1 || text[0].kind != "text" || text[0].body != authored {
		t.Fatalf("non-tool authored text changed: %+v", text)
	}
}

func TestAgentPaneEditLikeToolBlocksOmitDiffBodies(t *testing.T) {
	entry := activityPaneEntry{
		Kind: "tool",
		Text: "Edit `a.go` +1 -1\n```diff\n@@ -1 +1 @@\n-old\n+new\n```\n\n" +
			"Create `b.go` +1 -0 · cp\n```diff\n@@ -0,0 +1 @@\n+created\n```\n\n" +
			"Delete `c.go` +0 -1\n```diff\n@@ -1 +0,0 @@\n-removed\n```\n\n" +
			"Move `old.go` -> `new.go`\n```diff\n@@ -1 +1 @@\n-before\n+after\n```",
	}
	blocks := parseLiveActivity(entry)
	wantVerbs := []string{"Edit", "Create", "Delete", "Move"}
	wantLabels := []string{"`a.go` +1 -1", "`b.go` +1 -0 · cp", "`c.go` +0 -1", "`old.go` -> `new.go`"}
	if len(blocks) != len(wantVerbs) {
		t.Fatalf("edit-like blocks = %+v, want %d blocks", blocks, len(wantVerbs))
	}
	for i, block := range blocks {
		if block.kind != "op" || block.verb != wantVerbs[i] || block.label != wantLabels[i] {
			t.Errorf("block %d lost its operation/path/count label: %+v", i, block)
		}
		if block.fenced || block.code != "" || block.body != "" || strings.Contains(block.label, "diff") {
			t.Errorf("block %d retained diff content: %+v", i, block)
		}
	}
}

func TestAgentPaneChainedMultilineFallback(t *testing.T) {
	command := "cat a.go && python -c '\nprint(1)\n\nprint(2)\n'"
	blocks := parseLiveActivity(activityPaneEntry{Kind: "tool", Text: toolActivityShell(command)})
	if len(blocks) != 2 || blocks[0].kind != "reads" || blocks[1].verb != "Run" || !blocks[1].fenced || !strings.Contains(blocks[1].code, "print(1)\n\nprint(2)") {
		t.Fatalf("multiline && neighbor lost structure: %+v", blocks)
	}
}
