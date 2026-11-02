package launch

import (
	"bytes"
	"crypto/sha256"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	jsonv1 "encoding/json"

	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
	"github.com/yusing/mekugi/modules/fileread/read"
)

func TestDuplicateOutputNativeMCatRanges(t *testing.T) {
	for _, similar := range []bool{false, true} {
		t.Run(fmt.Sprintf("similar=%v", similar), func(t *testing.T) {
			// Each JSON row is a distinct, realistic reader record. Shared runs are
			// comfortably above the matcher threshold. Distinct checksums keep shifted
			// records from becoming cheaper character edits of otherwise identical rows.
			rows := make([]string, 16)
			for i := range rows {
				row := fmt.Sprintf("{\"id\":%d,\"path\":\"internal/router/record_%02d.go\",\"description\":\"Preserve original host execution evidence while projecting repeated reader output into request-local references.\"", i+1, i+1)
				if !similar {
					row += fmt.Sprintf(",\"checksum\":\"%x\"", sha256.Sum256([]byte(fmt.Sprint(i))))
				}
				rows[i] = row + "}\n"
			}
			for _, tc := range []struct {
				name, first, second, want string
				number                    bool
				edit                      bool
				similarEdits              bool
			}{
				{"trailing overlap", "1:10", "7:16", "See O1:7-10\n" + strings.Join(rows[10:], ""), false, false, true},
				{"leading overlap", "7:16", "1:10", strings.Join(rows[:6], "") + "See O1:1-4\n", false, false, true},
				{"contained", "1:16", "5:10", "See O1:5-10\n", false, false, false},
				{"container", "5:10", "1:16", strings.Join(rows[:4], "") + "See O1\n" + strings.Join(rows[10:], ""), false, false, false},
				{"edited and new rows", "1:10", "5:16", "See O1:5-6\n{\"id\":7,\"description\":\"Edited after the first read.\"}\nSee O1:8-10\n" + strings.Join(rows[10:], ""), false, true, false},
				{"absolute numbered overlap", "7:12", "10:16", "See O1:4-6\n", true, false, false},
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
						args, commandText := []string{path, span}, "mcat fixture.json "+span
						if tc.number {
							args = append([]string{"--number"}, args...)
							commandText = "mcat --number fixture.json " + span
						}
						result, err := (read.Service{}).Execute(t.Context(), command.Request{Args: args, Stdin: nil})
						if err != nil || result.ExitCode != 0 || result.Stderr != "" {
							t.Fatalf("mcat %v: %+v, %v", args, result, err)
						}
						return commandText, result.Stdout
					}
					firstCommand, first := read(tc.first)
					if tc.edit {
						edited := append([]string(nil), rows...)
						edited[6] = "{\"id\":7,\"description\":\"Edited after the first read.\"}\n"
						write(edited)
					}
					secondCommand, second := read(tc.second)
					want := tc.want
					if tc.name != "contained" {
						want = "O2:\n" + want
					}
					if tc.number {
						// The marker's :4-6 addresses output rows, while retained rows
						// keep the reader's absolute source numbering (13 through 16).
						for i := 12; i < len(rows); i++ {
							want += fmt.Sprintf("%6d\t%s", i+1, rows[i])
						}
					}
					input := append(duplicateTestInput(firstCommand, "first", first), duplicateTestInput(secondCommand, "second", second)...)
					proxy := newManagedMekugiProxy(t)
					proxy.inputProjections = testInputProjections()
					request := duplicateTestPrepare(t, proxy, input, codexresponses.TurnMetadata{})
					items := duplicateTestItems(t, request)
					if got := responses.StringField(items[3], "output"); similar && tc.similarEdits {
						assertCheaperSingleBaseProjection(t, got, duplicateTestHeader+want)
					} else if got != duplicateTestHeader+want {
						t.Fatalf("projected reader output = %q, want %q", got, duplicateTestHeader+want)
					}
					if responses.StringField(items[1], "output") != duplicateTestHeader+"O1:\n"+first || !responses.SameJSONValue(request.OriginalField("input"), responses.MustMarshalJSON(input)) {
						t.Fatal("original reader evidence changed")
					}
				})
			}
		})
	}
}

func TestDuplicateOutputNativeMCatThirdReadProviderPrefix(t *testing.T) {
	for _, similar := range []bool{false, true} {
		t.Run(fmt.Sprintf("similar=%v", similar), func(t *testing.T) {
			var rows []string
			for i := 1; i <= 16; i++ {
				row := fmt.Sprintf("// Record %02d: preserve original host execution evidence and keep request-local output references attached to earlier verbatim reader rows.", i)
				if !similar {
					row += fmt.Sprintf(" checksum %x", sha256.Sum256([]byte(fmt.Sprint(i))))
				}
				rows = append(rows, row+"\n")
			}
			path := filepath.Join(t.TempDir(), "fixture.go")
			if err := os.WriteFile(path, []byte(strings.Join(rows, "")), 0o600); err != nil {
				t.Fatal(err)
			}
			var input []any
			read := func(span, id string) {
				t.Helper()
				result, err := (read.Service{}).Execute(t.Context(), command.Request{Args: []string{path, span}, Stdin: nil})
				if err != nil || result.ExitCode != 0 || result.Stderr != "" {
					t.Fatalf("mcat %s: %+v, %v", span, result, err)
				}
				input = append(input, duplicateTestInput("mcat fixture.go "+span, id, result.Stdout)...)
			}
			proxy := newManagedMekugiProxy(t)
			proxy.inputProjections = testInputProjections()
			read("1:10", "first")
			read("7:16", "second")
			first := duplicateTestPrepare(t, proxy, input, codexresponses.TurnMetadata{})
			firstItems := duplicateTestItems(t, first)
			if got, want := responses.StringField(firstItems[3], "output"), duplicateTestHeader+"O2:\nSee O1:7-10\n"+strings.Join(rows[10:], ""); similar {
				assertCheaperSingleBaseProjection(t, got, want)
			} else if got != want {
				t.Fatalf("second read = %q, want %q", got, want)
			}
			prefix := bytes.Clone(first.Fields["input"])
			var original []jsonv1.RawMessage
			if err := json.Unmarshal(prefix, &original); err != nil {
				t.Fatal(err)
			}

			read("5:14", "third")
			grown := duplicateTestPrepare(t, proxy, input, codexresponses.TurnMetadata{})
			items := duplicateTestItems(t, grown)
			// Distinct records retain verbatim rows 11-14 at O2:2-5. The similar
			// second body became edits, so its rows cannot supply a new source.
			// This shorter third overlap keeps the cheaper ordinary projection,
			// with O2 now identifying its own newly retained rows 11-14.
			want := duplicateTestHeader + "See O1:5-10\nSee O2:2-5\n"
			if similar {
				want = duplicateTestHeader + "O2:\nSee O1:5-10\n" + strings.Join(rows[10:14], "")
			}
			if got := responses.StringField(items[5], "output"); got != want {
				t.Fatalf("third read = %q, want direct verbatim references %q", got, want)
			}
			var projected []jsonv1.RawMessage
			if err := json.Unmarshal(grown.Fields["input"], &projected); err != nil {
				t.Fatal(err)
			}
			if !responses.SameJSONValue(responses.MustMarshalJSON(projected[:len(original)]), prefix) {
				t.Fatal("third read changed the already projected provider prefix")
			}
			exchange := confirmedHistoryExchange(t, original, false)
			grown.Fields["previous_response_id"] = responses.MustMarshalJSON("parent")
			if _, err := exchange.Reconcile(grown, true); err != nil {
				t.Fatal(err)
			}
			if grown.CachedInput != len(original) || grown.RebaseInput {
				t.Fatal("third read rebased a confirmed provider prefix")
			}
		})
	}
}

func assertCheaperSingleBaseProjection(t *testing.T, projected, baseline string) {
	t.Helper()
	if !strings.HasPrefix(projected, duplicateTestHeader+"See O1 ") || strings.Count(projected, "See O") != 1 {
		t.Fatalf("expected one direct verbatim base: %q", projected)
	}
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	after, err := codec.Count(projected)
	if err != nil {
		t.Fatal(err)
	}
	before, err := codec.Count(baseline)
	if err != nil {
		t.Fatal(err)
	}
	if after >= before {
		t.Fatalf("single-base edits cost %d tokens, baseline %d", after, before)
	}
}
