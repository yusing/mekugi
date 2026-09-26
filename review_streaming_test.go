package mekugi

import (
	"strings"
	"testing"
)

func TestStreamingReviewKeepsReplacementRowsInSourceOrder(t *testing.T) {
	before := "package demo\nfunc launch() {\n\tif requested {\n\t\told()\n\t}\n\twait()\n}\n"
	old := "\tif requested {\n\t\told()\n\t}\n\twait()\n"
	previous := ""
	for _, next := range []string{
		"\tframe := wait()\n",
		"\tframe := wait()\n\tif requested {\n\t\tshow(frame)\n\t}\n",
		"\tframe := wait()\n\tif requested {\n\t\tshow(frame)\n\t}\n\treturn frame\n",
	} {
		file := RenderStreamingReviewFile("demo.go", "demo.go", before, strings.Replace(before, old, next, 1))
		var removed, added strings.Builder
		adding := false
		for row := range strings.SplitSeq(file.Diff, "\n") {
			if strings.HasPrefix(row, "---") || strings.HasPrefix(row, "+++") {
				continue
			}
			if strings.HasPrefix(row, "-") {
				if adding {
					t.Fatalf("deletion moved after additions:\n%s", file.Diff)
				}
				removed.WriteString(row[1:] + "\n")
			}
			if strings.HasPrefix(row, "+") {
				adding = true
				added.WriteString(row[1:] + "\n")
			}
		}
		if removed.String() != old || added.String() != next || !strings.HasPrefix(added.String(), previous) {
			t.Fatalf("replacement rows changed order:\n%s", file.Diff)
		}
		previous = next
	}
}
