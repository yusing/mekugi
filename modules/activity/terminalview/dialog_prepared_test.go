package terminalview

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/diffrender"
	activitymodel "github.com/yusing/mekugi/modules/activity/feed"
	"github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func TestUISnapshotPreparedOutputFormats(t *testing.T) {
	for _, theme := range []diffrender.Theme{diffrender.TerminalTheme, diffrender.DarkTheme, diffrender.LightTheme} {
		t.Run(fmt.Sprint(theme), func(t *testing.T) {
			p := Painter{Theme: theme}
			var rows []string
			for _, tc := range []struct {
				value presentation.Value
				text  string
			}{
				{presentation.Value{Content: activitymodel.Content{Kind: "reads", Verb: "Read", Reads: []activitymodel.Read{{Path: "main.go"}}}}, "package main\nvar answer = 42\n[same as O1]"},
				{presentation.Value{Content: activitymodel.Content{Kind: "op", Verb: "Attached skill"}}, "# Guide\n\nUse **retained** content.\n\n[same as O1]"},
				{presentation.Value{Content: activitymodel.Content{Kind: "op", Verb: "Run"}}, "--- a/file\n+++ b/file\n@@ -1 +1 @@\n-old\n+new"},
			} {
				prepared := p.DetailDocument(presentation.DetailPage{Value: tc.value, ModelVisible: true, Prepared: &presentation.PreparedContent{Text: tc.text, Altered: true}}, nil, nil, 64)
				output := activitymodel.OutputView{Done: true, Lines: strings.Split(tc.text, "\n")}
				original := p.DialogPage(Block{Value: tc.value, OutputSnapshot: &output}, 64)
				if !reflect.DeepEqual(prepared.Lines, original.Lines) || prepared.Text != tc.text {
					t.Fatalf("%s: prepared output lost original rendering or copy", tc.value.Verb)
				}
				rows = append(rows, tc.value.Verb)
				for i := range prepared.Lines {
					rows = append(rows, prepared.Rows(i, 64)...)
				}
				rows = append(rows, "plain after output")
			}
			uisnapshot.AssertTerminal(t, fmt.Sprintf("testdata/snapshots/prepared-output-%d.txt", theme), rows, 64)
		})
	}
}
