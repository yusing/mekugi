package activity_test

import (
	"reflect"
	"testing"

	"github.com/yusing/mekugi/internal/activity"
)

func TestParseOperations(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []activity.Content
	}{
		{
			name: "ordered reads and literal non-read operands",
			text: "Read `b.go 8:12 2:4` `a.go`\n\nSearch `b.go 8:12`\n\nInspect `a.go 2:4`",
			want: []activity.Content{
				{Kind: "reads", Verb: "Read", Reads: []activity.Read{{Path: "b.go", Ranges: []string{"8:12", "2:4"}}, {Path: "a.go"}}},
				{Kind: "reads", Verb: "Search", Reads: []activity.Read{{Path: "b.go 8:12"}}},
				{Kind: "reads", Verb: "Inspect", Reads: []activity.Read{{Path: "a.go 2:4"}}},
			},
		},
		{
			name: "fenced skill program preserves blank lines and rejects a short closing fence",
			text: "Skill `run example/script.sh`\n````sh\nprintf first\n\n```\nprintf last\n`````\n\nSkill `run example/other.sh`",
			want: []activity.Content{
				{Kind: "op", Verb: "Skill", Label: "`run example/script.sh`", Fenced: true, Lang: "sh", Code: "printf first\n\n```\nprintf last"},
				{Kind: "op", Verb: "Skill", Label: "`run example/other.sh`"},
			},
		},
		{
			name: "unknown operation retains its label and body",
			text: "Custom `operand` · detail\nretained body",
			want: []activity.Content{{Kind: "op", Verb: "Custom", Label: "`operand` · detail", Body: "retained body"}},
		},
		{
			name: "edit receipts inherit adjacent sources and mark source changes",
			text: "Edit `a.go` · +1 −0 · apply_patch\n\n+ 2 tool-managed files (b.go, c.go)\n\n+ 1 tool-managed files (d.go) · python3\n\nEdit · python3",
			want: []activity.Content{
				{Kind: "op", Verb: "Edit", Label: "`a.go` · +1 −0 · apply_patch", EditSource: "apply_patch", EditHeader: true},
				{Kind: "op", Verb: "Edit", Label: "2 tool-managed files (b.go, c.go) · apply_patch", EditSource: "apply_patch"},
				{Kind: "op", Verb: "Edit", Label: "1 tool-managed files (d.go) · python3", EditSource: "python3", EditHeader: true},
				{Kind: "op", Verb: "Edit", Label: "paths unavailable · python3", EditSource: "python3"},
			},
		},
		{
			name: "intervening operations end edit source inheritance",
			text: "Edit `a.go` · apply_patch\n\nRead `a.go`\n\n+ 1 tool-managed files (b.go)",
			want: []activity.Content{
				{Kind: "op", Verb: "Edit", Label: "`a.go` · apply_patch", EditSource: "apply_patch", EditHeader: true},
				{Kind: "reads", Verb: "Read", Reads: []activity.Read{{Path: "a.go"}}},
				{Kind: "op", Verb: "Edit", Label: "1 tool-managed files (b.go)"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := activity.ParseOperations(tt.text); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseOperations() = %#v; want %#v", got, tt.want)
			}
		})
	}
}

func TestAddTimeoutsPreservesOperationContent(t *testing.T) {
	text := "Read `a.go 2:4` (timeout 5s)\n\nRun `script`\n```sh\nprintf '(timeout 5s)'\n\nprintf done\n```"
	want := "Read `a.go 2:4` (timeout 5s) (timeout 1m)\n\nRun `script` (timeout 5s) (timeout 1m)\n```sh\nprintf '(timeout 5s)'\n\nprintf done\n```"
	got := activity.AddTimeouts(text, []string{"5s", "1m", "5s"})
	if got != want {
		t.Fatalf("AddTimeouts() = %q; want %q", got, want)
	}
	if again := activity.AddTimeouts(got, []string{"5s", "1m"}); again != got {
		t.Fatalf("repeated AddTimeouts() = %q; want unchanged %q", again, got)
	}
	before, after := activity.ParseOperations(text), activity.ParseOperations(got)
	if len(after) != len(before) {
		t.Fatalf("operation count = %d; want %d", len(after), len(before))
	}
	for i := range after {
		if !reflect.DeepEqual(after[i].Timeouts, []string{"5s", "1m"}) {
			t.Errorf("operation %d timeouts = %q", i, after[i].Timeouts)
		}
		after[i].Timeouts, before[i].Timeouts = nil, nil
		if !reflect.DeepEqual(after[i], before[i]) {
			t.Errorf("operation %d content changed: got %#v; want %#v", i, after[i], before[i])
		}
	}
}

func TestParseOperationsOmitsEditDiffButParseOperationRetainsFallback(t *testing.T) {
	text := "Edit `a.go` · apply_patch\n```diff\n-old\n+new\n```"
	want := activity.Content{Kind: "op", Verb: "Edit", Label: "`a.go` · apply_patch", Code: "-old\n+new", Lang: "diff", Fenced: true}
	if got := activity.ParseOperation(text); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseOperation() = %#v; want native diff fallback %#v", got, want)
	}
	want.Code, want.Lang, want.Fenced = "", "", false
	want.EditSource, want.EditHeader = "apply_patch", true
	if got := activity.ParseOperations(text); !reflect.DeepEqual(got, []activity.Content{want}) {
		t.Fatalf("ParseOperations() = %#v; want omitted edit body %#v", got, want)
	}
}
