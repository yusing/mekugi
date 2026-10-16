package router

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/changehistory"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestMChangesRecoveryPublicationFailurePreservesOutput(t *testing.T) {
	for _, mode := range []string{"paged", "partial", "mutation"} {
		t.Run(mode, func(t *testing.T) {
			f := newMChangesSliceFixture(t, "recovery-publication-"+mode)
			t.Setenv("CODEX_THREAD_ID", f.thread)
			id := f.reserve(t, f.thread, "change")
			var files []diffmodel.ReviewFile
			for i := 0; i < 20; i++ {
				name := fmt.Sprintf("change-%02d.txt", i)
				files = append(files, diffmodel.RenderReviewFile(name, name, "before\n", "after\n"))
				if mode == "mutation" {
					if err := os.WriteFile(filepath.Join(f.workspace, name), []byte("after\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			f.publish(t, id, "change", "change-call", mekugiHistory{ReviewFiles: files})
			// Reject only recovery publication, after the real frontend has admitted output.
			kinds := append(RetainedRecordKinds(), recovery.RecordKinds()...)
			for i := range kinds {
				if kinds[i].ID == "output" {
					kinds[i].MaxRecordBytes = 1
				}
			}
			faultHistory, err := history.NewService(kinds)
			if err != nil {
				t.Fatal(err)
			}
			faultRecovery, err := recovery.New(faultHistory, changehistory.Module.Contributes.References)
			if err != nil {
				t.Fatal(err)
			}
			run := func(args []string) (string, string, int) {
				var out, diag bytes.Buffer
				handled, status := runToolPluginWorkerInHostProcess(testHistory(), faultRecovery, testCommands(), t.Context(), f.registry.Frontends["mchanges"], args, nil, &out, &diag)
				if !handled {
					t.Fatal("worker not recognized")
				}
				return out.String(), diag.String(), status
			}
			base := []string{"--workspace", f.workspace, id}
			full, diag, status := run(append(append([]string{}, base...), "--max-tokens", "15500"))
			if status != 0 || full == "" || diag != "" {
				t.Fatalf("complete read: status=%d stdout=%q stderr=%q", status, full, diag)
			}
			args := append(append([]string{}, base...), "--max-tokens", "12")
			if mode == "partial" {
				args = append(args, "amber999999")
			}
			if mode == "mutation" {
				args = append([]string{"revert"}, args...)
			}
			out, diag, status := run(args)
			if strings.Contains(out+diag, "next_call") {
				t.Fatal("unpublished reference exposed")
			}
			if mode == "mutation" {
				if status != 0 {
					t.Errorf("mutation status=%d, want 0", status)
				}
				if !strings.Contains(out, "change-19.txt clean") || !strings.Contains(diag, "report not paged") {
					t.Fatalf("mutation full report missing: stdout=%q stderr=%q", out, diag)
				}
				if data, err := os.ReadFile(filepath.Join(f.workspace, "change-19.txt")); err != nil || string(data) != "before\n" {
					t.Fatalf("mutation missing: %q %v", data, err)
				}
				return
			}
			if status != 1 {
				t.Errorf("paged read status=%d, want 1", status)
			}
			selected, err := testChanges().Execute(f.ctx, command.Request{Args: args, Scope: retainedScope(f.ctx, f.store.directory)})
			admitted := selected.Stdout
			if err != nil || admitted == "" || admitted == full {
				t.Fatalf("invalid bounded fixture: err=%v admitted=%q", err, admitted)
			}
			if out != admitted {
				t.Errorf("lost admitted output: got %q want %q", out, admitted)
			}
			if mode == "partial" && !strings.Contains(diag, "amber999999") {
				t.Errorf("lost original target diagnostic: %q", diag)
			}
			if !strings.Contains(diag, "recovery unavailable:") {
				t.Errorf("missing recovery-unavailable diagnostic: %q", diag)
			}
		})
	}
}
