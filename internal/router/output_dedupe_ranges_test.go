package router

import (
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/router/toolplugin"
)

func TestDuplicateOutputNativeMCatRanges(t *testing.T) {
	// Each JSON row is a distinct, realistic reader record. Shared runs are
	// comfortably above the matcher threshold, without testing its bounds.
	rows := make([]string, 16)
	for i := range rows {
		rows[i] = fmt.Sprintf("{\"id\":%d,\"path\":\"internal/router/record_%02d.go\",\"description\":\"Preserve original host execution evidence while projecting repeated reader output into request-local references.\"}\n", i+1, i+1)
	}
	for _, tc := range []struct {
		name, first, second, want string
		number                    bool
		edit                      bool
	}{
		{"trailing overlap", "1:10", "7:16", "[same as `mcat fixture.json 1:10` L7-10]\n" + strings.Join(rows[10:], ""), false, false},
		{"leading overlap", "7:16", "1:10", strings.Join(rows[:6], "") + "[same as `mcat fixture.json 7:16` L1-4]\n", false, false},
		{"contained", "1:16", "5:10", "[same as `mcat fixture.json 1:16` L5-10]\n", false, false},
		{"container", "5:10", "1:16", strings.Join(rows[:4], "") + "[same as `mcat fixture.json 5:10`]\n" + strings.Join(rows[10:], ""), false, false},
		{"edited and new rows", "1:10", "5:16", "[same as `mcat fixture.json 1:10` L5-6]\n{\"id\":7,\"description\":\"Edited after the first read.\"}\n[same as `mcat fixture.json 1:10` L8-10]\n" + strings.Join(rows[10:], ""), false, true},
		{"absolute numbered overlap", "7:12", "10:16", "[same as `mcat --number fixture.json 7:12` L4-6]\n", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fixture.json")
			write := func(source []string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(strings.Join(source, "")), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write(rows)
			read := func(span string) (string, string) {
				t.Helper()
				args, command := []string{path, span}, "mcat fixture.json "+span
				if tc.number {
					args = append([]string{"--number"}, args...)
					command = "mcat --number fixture.json " + span
				}
				result, err := toolplugin.ExecuteBuiltin(t.Context(), "mcat", args)
				if err != nil || result.ExitCode != 0 || result.Stderr != "" {
					t.Fatalf("mcat %v: %+v, %v", args, result, err)
				}
				return command, result.Stdout
			}
			firstCommand, first := read(tc.first)
			if tc.edit {
				edited := append([]string(nil), rows...)
				edited[6] = "{\"id\":7,\"description\":\"Edited after the first read.\"}\n"
				write(edited)
			}
			secondCommand, second := read(tc.second)
			want := tc.want
			if tc.number {
				// The marker's L4-6 addresses output rows, while retained rows
				// keep the reader's absolute source numbering (13 through 16).
				for i := 12; i < len(rows); i++ {
					want += fmt.Sprintf("%6d\t%s", i+1, rows[i])
				}
			}
			input := append(duplicateTestInput(firstCommand, "first", first), duplicateTestInput(secondCommand, "second", second)...)
			proxy := newManagedMekugiProxy(t)
			proxy.duplicateOutput = true
			request := duplicateTestPrepare(t, proxy, input, codexTurnMetadata{})
			items := duplicateTestItems(t, request)
			if got := jsonString(items[3], "output"); got != duplicateTestHeader+want {
				t.Fatalf("projected reader output = %q, want %q", got, duplicateTestHeader+want)
			}
			if jsonString(items[1], "output") != duplicateTestHeader+first || !sameJSONValue(request.originalFields["input"], mustMarshalJSON(input)) {
				t.Fatal("original reader evidence changed")
			}
		})
	}
}

func TestDuplicateOutputNativeMCatThirdReadProviderPrefix(t *testing.T) {
	var rows []string
	for i := 1; i <= 16; i++ {
		rows = append(rows, fmt.Sprintf("// Record %02d: preserve original host execution evidence and keep request-local output references attached to earlier verbatim reader rows.\n", i))
	}
	path := filepath.Join(t.TempDir(), "fixture.go")
	if err := os.WriteFile(path, []byte(strings.Join(rows, "")), 0o600); err != nil {
		t.Fatal(err)
	}
	var input []any
	read := func(span, id string) {
		t.Helper()
		result, err := toolplugin.ExecuteBuiltin(t.Context(), "mcat", []string{path, span})
		if err != nil || result.ExitCode != 0 || result.Stderr != "" {
			t.Fatalf("mcat %s: %+v, %v", span, result, err)
		}
		input = append(input, duplicateTestInput("mcat fixture.go "+span, id, result.Stdout)...)
	}
	proxy := newManagedMekugiProxy(t)
	proxy.duplicateOutput = true
	read("1:10", "first")
	read("7:16", "second")
	first := duplicateTestPrepare(t, proxy, input, codexTurnMetadata{})
	firstItems := duplicateTestItems(t, first)
	if got, want := jsonString(firstItems[3], "output"), duplicateTestHeader+"[same as `mcat fixture.go 1:10` L7-10]\n"+strings.Join(rows[10:], ""); got != want {
		t.Fatalf("second read = %q, want %q", got, want)
	}
	prefix := bytes.Clone(first.fields["input"])
	var original []jsonv1.RawMessage
	if err := json.Unmarshal(prefix, &original); err != nil {
		t.Fatal(err)
	}
	state, err := (providerHistory{confirmed: true}).append(original)
	if err != nil {
		t.Fatal(err)
	}
	read("5:14", "third")
	grown := duplicateTestPrepare(t, proxy, input, codexTurnMetadata{})
	items := duplicateTestItems(t, grown)
	// Rows 11-14 are verbatim at L2-5 of the second projected body, after
	// its overlap became one marker. The third read must address those rows.
	want := duplicateTestHeader + "[same as `mcat fixture.go 1:10` L5-10]\n[same as `mcat fixture.go 7:16` L2-5]\n"
	if got := jsonString(items[5], "output"); got != want {
		t.Fatalf("third read = %q, want direct verbatim references %q", got, want)
	}
	var projected []jsonv1.RawMessage
	if err := json.Unmarshal(grown.fields["input"], &projected); err != nil {
		t.Fatal(err)
	}
	if !sameJSONValue(mustMarshalJSON(projected[:len(original)]), prefix) {
		t.Fatal("third read changed the already projected provider prefix")
	}
	exchange := &webSocketExchange{parentID: "parent", history: &webSocketHistory{parent: &webSocketHistory{providerHistory: state}}}
	grown.fields["previous_response_id"] = mustMarshalJSON("parent")
	if err := exchange.reconcileProviderHistory(grown, mustMarshalJSON(grown.fields)); err != nil {
		t.Fatal(err)
	}
	if grown.cachedInput != len(original) || grown.rebaseInput {
		t.Fatal("third read rebased a confirmed provider prefix")
	}
}
