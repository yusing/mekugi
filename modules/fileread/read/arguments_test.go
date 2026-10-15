package read

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestReadBundleValidation(t *testing.T) {
	for _, args := range [][]string{nil, {"--max-tokens", "0", "a"}, {"a", "2:1"}, {"a", "01:2"}, {"a", "1:9007199254740992"}, {"a", ""}, {"a", "--max-tokens", "2000", "--max-tokens", "2000"}, {"-n", "1", "a", "b"}, {"-n", "0", "a"}, {"-n", "1", "-n", "2", "a"}, {"--tail", "a"}, {"--tail", "--tail", "-n", "1", "a"}, {"--tail", "a", "b"}, {"--max-tokens"}, {"-n"}, slices.Repeat([]string{"a"}, 17)} {
		if _, _, err := Parse(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	specs, budget, err := Parse([]string{"a b", "1:20", "--max-tokens", "2000", "other", "third", "200:300"})
	if err != nil || budget != 2000 || len(specs) != 3 || specs[0].Path != "a b" || specs[0].Range != "1:20" || specs[1].Range != "" || specs[2].Range != "200:300" {
		t.Fatalf("%+v %d %v", specs, budget, err)
	}
}

func TestReadBundleDefaultMultiFileBudget(t *testing.T) {
	specs, budget, err := Parse([]string{"first.go", "second.go"})
	if err != nil || budget != 6000 || len(specs) != 2 {
		t.Fatalf("parse default multi-file bundle: specs=%+v budget=%d err=%v", specs, budget, err)
	}
}

func TestReadBundleLimitReportsFullReadCount(t *testing.T) {
	for _, count := range []int{16, 17, 21, 33} {
		args := append([]string{"--max-tokens=2000"}, slices.Repeat([]string{"source"}, count)...)
		specs, budget, err := Parse(args)
		if count == 16 {
			if err != nil || len(specs) != count || budget != 2000 {
				t.Fatalf("limit boundary: specs=%+v budget=%d err=%v", specs, budget, err)
			}
			continue
		}
		want := fmt.Sprintf("received %d reads after argument expansion, exceeding the 16-read limit; split into %d mcat calls of at most 16 reads each; repeat the path when splitting its ranges", count, (count+15)/16)
		if err == nil || err.Error() != want {
			t.Fatalf("%d paths: error=%v, want %q", count, err, want)
		}
	}
	args := []string{"first", "1:1", "2:2"}
	args = append(args, slices.Repeat([]string{"other"}, 19)...)
	_, _, err := Parse(args)
	if err == nil || !strings.HasPrefix(err.Error(), "received 21 reads after argument expansion,") {
		t.Fatalf("mixed paths and ranges: error=%v", err)
	}
}

func TestParseReadBundleNormalizesColonAndDashSpansPerPath(t *testing.T) {
	specs, _, err := Parse([]string{"first", "1:3", "4-6", "second", "7-9"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Selection{
		{Path: "first", Range: "1:3"},
		{Path: "first", Range: "4:6"},
		{Path: "second", Range: "7:9"},
	}
	if len(specs) != len(want) {
		t.Fatalf("spec count = %d, want %d: %+v", len(specs), len(want), specs)
	}
	for i := range want {
		if specs[i].Path != want[i].Path || specs[i].Range != want[i].Range {
			t.Fatalf("spec %d = %+v, want %+v", i, specs[i], want[i])
		}
	}
}

func TestParseReadBundleCountsMultipleRangesTowardSixteenReadLimit(t *testing.T) {
	args := []string{"source"}
	for read := 1; read <= 16; read++ {
		args = append(args, fmt.Sprintf("%d-%d", read, read))
	}
	specs, _, err := Parse(args)
	if err != nil || len(specs) != 16 {
		t.Fatalf("16 ranges for one path: specs=%+v err=%v", specs, err)
	}
	args = append(args, "17:17")
	want := "received 17 reads after argument expansion, exceeding the 16-read limit; split into 2 mcat calls of at most 16 reads each; repeat the path when splitting its ranges"
	if _, _, err := Parse(args); err == nil || err.Error() != want {
		t.Fatalf("17 ranges: error=%v, want %q", err, want)
	}
}
