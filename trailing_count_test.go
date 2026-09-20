package mekugi

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInlineTrailingTargetCount(t *testing.T) {
	for _, operation := range []string{"type", "add"} {
		for _, target := range []string{`"old"`, fmt.Sprintf(`1:%s "old"`, hashLine("old old old"))} {
			t.Run(operation+target, func(t *testing.T) {
				prefix := operation + " " + target
				want, err := EditText(t.Context(), "old old old", prefix+` 2 "new"`)
				if err != nil {
					t.Fatal(err)
				}
				got, err := EditText(t.Context(), "old old old", prefix+` "new" 2`)
				if err != nil || got != want {
					t.Fatalf("trailing count = %q, %v; canonical = %q", got, err, want)
				}
			})
		}
	}
}

func TestInlineTrailingTargetCountRejectsAmbiguity(t *testing.T) {
	for _, script := range []string{
		`type "old" 1 "new" 2`,
		`type "old" 2 "new" 2`,
		`type "old" "new" 0`,
		`type "old" "new" -1`,
		`type "old" "new" 01`,
		`type "old" "new" 99999999999999999999999999999`,
		`type "old" "new" 2 extra`,
		`type "old" "new"2`,
		`type 1:1234 "new" 2`,
		`append "new" 2`,
	} {
		if _, err := parse(script); err == nil {
			t.Errorf("accepted ambiguous or invalid count: %s", script)
		}
	}
}

func TestInlineTrailingCountMissingOccurrencesIsAtomic(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "file.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := ApplyForHostAt(t.Context(), workspace, []FileEdit{{Path: "file.txt", Script: "append \"prefix\"\ntype \"old\" \"new\" 2"}}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "occurrence") {
		t.Fatalf("missing occurrences did not reject: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "old\n" {
		t.Fatalf("rejection changed baseline: %q, %v", content, err)
	}
}
