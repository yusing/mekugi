package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/patchtest"
)

func TestRunMekugiAcceptsFinalStateReportAndPreservesUnrelatedFiles(t *testing.T) {
	scenario := scenario{
		initial: map[string]string{
			"target.txt":    "old\n",
			"unrelated.txt": "keep\n",
		},
		edits: []mekugi.FileEdit{{Path: "target.txt", Script: "type 1:cba0 \"old\" \"new\"\n"}},
	}
	got, err := runMekugi(scenario)
	if err != nil {
		t.Fatal(err)
	}
	if got["target.txt"] != "new\n" || got["unrelated.txt"] != "keep\n" || len(got) != 2 {
		t.Fatalf("tree = %#v", got)
	}
}

func TestRunMekugiRejectsMalformedAndFutureCommands(t *testing.T) {
	for _, script := range []string{
		"type 1:cba0\n",
		"future-command\n",
	} {
		t.Run(strings.TrimSpace(script), func(t *testing.T) {
			_, err := runMekugi(scenario{initial: map[string]string{"target.txt": "old\n"}, edits: []mekugi.FileEdit{{Path: "target.txt", Script: script}}})
			if err == nil || !strings.Contains(err.Error(), "applying HPATCH script") {
				t.Fatalf("runMekugi() error = %v", err)
			}
		})
	}
}

func TestScenariosProduceEquivalentChanges(t *testing.T) {
	for _, scenario := range scenarios() {
		t.Run(scenario.name, func(t *testing.T) {
			got, err := runMekugi(scenario)
			if err != nil {
				t.Fatal(err)
			}
			want, err := patchtest.Apply(scenario.initial, scenario.patch)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("mekugi tree = %#v, apply_patch tree = %#v", got, want)
			}
		})
	}
}
