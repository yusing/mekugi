package recovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/libraries/commandargs"
	"github.com/yusing/mekugi/libraries/outputbudget/tokenizer"
	"github.com/yusing/mekugi/modules/outputrecovery/internal/paging"
	"github.com/yusing/mekugi/modules/outputrecovery/internal/record"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func openTestRecovery(t *testing.T, directory string) *Session {
	t.Helper()
	histories, err := history.NewService(RecordKinds())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := histories.Open(t.Context(), directory); err != nil {
		t.Fatal(err)
	}
	service, err := New(histories, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Attach(scope.Retained{Directory: directory}, DefaultLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return session
}
func (s *Session) putShellOutput(ctx context.Context, stdout, stderr string, exit int) (string, error) {
	return s.Publish(ctx, command.OmittedOutput{Stdout: stdout, Stderr: stderr}, exit)
}
func TestRecoveryPagesAndRestart(t *testing.T) {
	store := openTestRecovery(t, t.TempDir())
	out, diagnostic := strings.Repeat("out π🙂\n", 36), strings.Repeat("err 引用\n", 30)
	id, err := store.putShellOutput(t.Context(), out, diagnostic, 7)
	if err != nil {
		t.Fatal(err)
	}
	reopened := openTestRecovery(t, store.scope.Directory)
	record, err := reopened.read(t.Context(), id)
	if err != nil || record.Stdout != out || record.Stderr != diagnostic || record.ExitCode != 7 {
		t.Fatalf("restart record: %#v, %v", record, err)
	}
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	const pageBudget = 96
	for _, selection := range []string{"", "stdout", "stderr"} {
		t.Run(selection, func(t *testing.T) {
			position := [2]int{}
			var gotOut, gotErr strings.Builder
			for pageIndex := range 100 {
				page, err := paging.Page(ctx, paging.ReadRequest{Stdout: out, Stderr: diagnostic, Position: position, Stream: selection}, pageBudget)
				if err != nil {
					t.Fatal(err)
				}
				if count, err := codec.Count(page.Text); err != nil || count > pageBudget {
					t.Fatalf("page budget = %d, %v", count, err)
				}

				frame := page.Text
				if after, ok := strings.CutPrefix(frame, "[stdout bytes]\n"); ok {
					part, rest, found := strings.Cut(after, "\n[/stdout]\n")
					if !found {
						t.Fatalf("missing stdout frame: %q", frame)
					}
					gotOut.WriteString(part)
					frame = rest
				}
				if after, ok := strings.CutPrefix(frame, "[stderr bytes]\n"); ok {
					part, found := strings.CutSuffix(after, "\n[/stderr]\n")
					if !found {
						t.Fatalf("missing stderr frame: %q", frame)
					}
					gotErr.WriteString(part)
				} else {
					gotOut.WriteString(frame)
				}

				if page.Complete {
					wantOut, wantErr := out, diagnostic
					if selection == "stdout" {
						wantErr = ""
					} else if selection == "stderr" {
						wantOut = ""
					}
					minimumPageIndex := 1
					if selection == "" {
						minimumPageIndex = 2
					}
					if pageIndex < minimumPageIndex || gotOut.String() != wantOut || gotErr.String() != wantErr {
						t.Fatalf("incomplete or duplicated pages: %d, %q, %q", pageIndex, gotOut.String(), gotErr.String())
					}
					return
				}
				ref, err := reopened.putCursor(ctx, record, page.Position, selection)
				if err != nil {
					t.Fatal(err)
				}
				cursor, err := reopened.read(ctx, ref)
				if err != nil || cursor.Position != page.Position || cursor.Stream != selection {
					t.Fatalf("cursor round trip: %#v, %v", cursor, err)
				}
				position = page.Position
			}
			t.Fatal("pagination did not finish")
		})
	}

}
func TestShellOutputReadRejectsInvalidState(t *testing.T) {
	t.Parallel()
	store := openTestRecovery(t, t.TempDir())
	id, err := store.putShellOutput(t.Context(), "same", "same", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, flags := range []string{
		"", "../outside", id + " --stdout --stderr",
		id + " --max-tokens 0", id + " --max-tokens 01", id + " --max-tokens 15501",
		id + " --cursor", id + " --tail", id + " --stdout --stdout",
	} {
		if _, err := parseOutputRead(strings.Fields(flags)); err == nil {
			t.Errorf("accepted %q", flags)
		}
	}
	if _, err := store.read(t.Context(), "r_"+strings.Repeat("A", 22)); err == nil {
		t.Fatal("missing record accepted")
	}
	name := filepath.Join(store.scope.Directory, outputName("", id))
	if err := os.WriteFile(name, []byte(`{"version":2,"id":"`+id+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.read(t.Context(), id); err == nil {
		t.Fatal("corrupt record accepted")
	}
	if _, err := store.putShellOutput(t.Context(), strings.Repeat("x", record.MaxOutputBytes+1), "", 0); err == nil {
		t.Fatal("oversize output accepted")
	}
	if _, err := store.putShellOutput(t.Context(), "\xff", "", 0); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if _, err := store.putCursor(t.Context(), record.Record{Version: 1, ID: id}, [2]int{-1, 0}, ""); err == nil {
		t.Fatal("negative cursor position accepted")
	}
}

func TestParseOutputReadDefaultBudgetAndOverride(t *testing.T) {
	defaults, err := parseOutputRead([]string{"amber"})
	if err != nil || defaults.maxTokens != 8000 {
		t.Fatalf("default output-read budget: options=%+v err=%v", defaults, err)
	}
	override, err := parseOutputRead([]string{"amber", "--max-tokens", "12000"})
	if err != nil || override.maxTokens != 12000 {
		t.Fatalf("explicit output-read budget: options=%+v err=%v", override, err)
	}
}

func TestShellOutputStoreQuotaAndPermissions(t *testing.T) {
	t.Parallel()
	store := openTestRecovery(t, t.TempDir())
	id, err := store.putShellOutput(t.Context(), "omitted", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(store.scope.Directory, outputName("", id)))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("record permissions: %v, %v", info, err)
	}
	// A sparse fixture exercises the separate quota without large allocations.
	// Like managed publishers, invalidate admission before changing store files.
	if err := store.store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		if err := tx.AdvanceRevision(); err != nil {
			return err
		}
		file, err := os.Create(filepath.Join(store.scope.Directory, outputName("", "maple")))
		if err != nil {
			return err
		}
		return errors.Join(file.Truncate(256<<20), file.Close())
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.putShellOutput(t.Context(), "next", "", 0); err == nil {
		t.Fatal("quota overflow accepted")
	}
	if record, err := store.read(t.Context(), id); err != nil || record.Stdout != "omitted" {
		t.Fatalf("quota evicted prior record: %#v, %v", record, err)
	}
	for _, budget := range []int{1, 64, 15500} {
		if _, err := parseOutputRead([]string{id, "--max-tokens", strconv.Itoa(budget)}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestShellOutputReadRejectsMissingAndNullFields(t *testing.T) {
	t.Parallel()
	store := openTestRecovery(t, t.TempDir())
	id, err := store.putShellOutput(t.Context(), "out", "err", 7)
	if err != nil {
		t.Fatal(err)
	}
	for _, fields := range []string{
		"",
		`,"stderr":"","exit_code":0`,
		`,"stdout":"","exit_code":0`,
		`,"stdout":"","stderr":""`,
		`,"stdout":null,"stderr":"","exit_code":0`,
		`,"stdout":"","stderr":null,"exit_code":0`,
		`,"stdout":"","stderr":"","exit_code":null`,
	} {
		data := `{"version":1,"id":"` + id + `"` + fields + `}`
		if err := os.WriteFile(filepath.Join(store.scope.Directory, outputName("", id)), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.read(t.Context(), id); err == nil || !strings.Contains(err.Error(), "missing required fields") {
			t.Fatalf("corrupt evidence accepted: %q, %v", fields, err)
		}
	}
}

func TestReadCursorRejectsAlteredPosition(t *testing.T) {
	t.Parallel()
	store := openTestRecovery(t, t.TempDir())
	id, err := store.putShellOutput(t.Context(), "first\nsecond\nthird\n", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.read(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.putCursor(t.Context(), source, [2]int{6, 0}, "stdout")
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(store.scope.Directory, outputName("", ref))
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, position := range []string{`"position":[0,0]`, `"position":[13,0]`, `"unused_position":[6,0]`} {
		altered := strings.Replace(string(data), `"position":[6,0]`, position, 1)
		if err := os.WriteFile(name, []byte(altered), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.read(t.Context(), ref); err == nil {
			t.Fatalf("altered cursor accepted: %s", position)
		}
	}
}

func TestShortReadCursorSurvivesRestart(t *testing.T) {
	store := openTestRecovery(t, t.TempDir())
	id, err := store.putShellOutput(t.Context(), "output", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.read(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	cursor, err := store.putCursor(t.Context(), source, [2]int{1, 0}, "stdout")
	if err != nil {
		t.Fatal(err)
	}
	reopened := openTestRecovery(t, store.scope.Directory)
	again, err := reopened.putCursor(t.Context(), source, [2]int{1, 0}, "stdout")
	if err != nil || cursor != again {
		t.Fatalf("cursor changed after restart: %q != %q, %v", cursor, again, err)
	}
}

func TestInlineBudgets(t *testing.T) {
	for _, args := range [][]string{{"--max-tokens=42"}, {"--max-tokens", "42"}} {
		options, err := parseOutputRead(append(args, "amber"))
		if err != nil || options.maxTokens != 42 {
			t.Fatalf("%v: %+v %v", args, options, err)
		}
	}
	for _, args := range [][]string{{"--max-tokens="}, {"--max-tokens=0"}, {"--max-tokens=01"}, {"--max-tokens=15501"}, {"--max-tokens=x"}, {"--max-tokens=4", "--max-tokens", "5"}} {
		if _, err := parseOutputRead(append(args, "amber")); err == nil || err.Error() != commandargs.MaxTokensArgumentError {
			t.Fatalf("%v: %v", args, err)
		}
	}
}
