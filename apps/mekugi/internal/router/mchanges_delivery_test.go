package router

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

func deliveryMutationFixture(t *testing.T) (*mchangesSliceFixture, string, string) {
	t.Helper()
	f := newMChangesSliceFixture(t, "mutation-delivery")
	id := f.reserve(t, f.thread, "change")
	var files []diffmodel.ReviewFile
	var report strings.Builder
	fmt.Fprintf(&report, "undo: mchanges apply %s\n", id)
	for i := range 20 {
		name := fmt.Sprintf("change-%02d.txt", i)
		files = append(files, diffmodel.RenderReviewFile(name, name, "before\n", "after\n"))
		if err := os.WriteFile(filepath.Join(f.workspace, name), []byte("after\n"), 0600); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&report, "   %s clean\n", name)
	}
	f.publish(t, id, "change", "change-call", mekugiHistory{ReviewFiles: files})
	return f, id, report.String()
}

func TestMChangesDeliveryMutationContinuationDoesNotRepeatEffect(t *testing.T) {
	t.Parallel()
	f, id, want := deliveryMutationFixture(t)
	out, diag, status := f.run(t, "mchanges revert "+id+" --max-tokens 12")
	const marker = "read: incomplete; next_call: mread "
	if status != 1 || out == "" || !strings.HasPrefix(diag, marker) {
		t.Fatalf("first page: stdout=%q stderr=%q status=%d", out, diag, status)
	}
	for i := range 20 {
		path := filepath.Join(f.workspace, fmt.Sprintf("change-%02d.txt", i))
		if got := readTestFile(t, path); got != "before\n" {
			t.Fatalf("mutation did not complete before publication: %s=%q", path, got)
		}
		// Restoring the original after-state makes a replayed revert observable.
		if err := os.WriteFile(path, []byte("after\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cursor := strings.TrimSpace(strings.TrimPrefix(diag, marker))
	remainder, diag, status := f.run(t, "mread "+cursor+" --stdout --max-tokens 15500")
	if status != 0 || diag != "" || out+remainder != want {
		t.Fatalf("retained report: stdout=%q stderr=%q status=%d; want %q", out+remainder, diag, status, want)
	}
	for i := range 20 {
		path := filepath.Join(f.workspace, fmt.Sprintf("change-%02d.txt", i))
		if got := readTestFile(t, path); got != "after\n" {
			t.Fatalf("continuation repeated mutation: %s=%q", path, got)
		}
	}
}

func TestMChangesDeliveryReadPreparationUsesReservedReferencesRepeatably(t *testing.T) {
	t.Parallel()
	f := newMChangesSliceFixture(t, "read-delivery")
	id := f.reserve(t, f.thread, "read")
	f.publish(t, id, "read", "read-call", mekugiHistory{Script: strings.Repeat("original source row\n", 100)})
	full, err := f.store.changes.Read(f.ctx, f.store.scoped(f.ctx).changeScope(), changes.Query{Workspace: f.workspace, IDs: []string{id}, View: "history"})
	if err != nil {
		t.Fatal(err)
	}
	output, err := f.store.changes.Execute(f.ctx, command.Request{Args: []string{"--workspace", f.workspace, "--history", "--max-tokens", "12", id}, Scope: retainedScope(f.ctx, f.store.directory)})
	if err != nil || output.Delivery == nil {
		t.Fatalf("no frozen read delivery: %+v %v", output, err)
	}
	plan := output.Delivery
	session, err := f.store.recoverySession(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	handles, err := session.Reserve(f.ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	first, err := plan.Prepare(handles[:1])
	if err != nil {
		t.Fatal(err)
	}
	second, err := plan.Prepare(handles[1:])
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := plan.Prepare(handles[:1])
	if err != nil || !reflect.DeepEqual(first, repeated) {
		t.Fatalf("preparation changed the captured plan or repeated result: %v", err)
	}
	if !reflect.DeepEqual(first.Recovery, second.Recovery) || first.Referenced.Stdout != output.Stdout || first.Unreferenced.Stderr != "" {
		t.Fatal("reference substitution changed admitted output or frozen recovery")
	}
	for i, prepared := range []command.PreparedDelivery{first, second} {
		if want := "read: incomplete; next_call: mread " + handles[i] + "\n"; prepared.Referenced.Stderr != want {
			t.Fatalf("reference: got %q want %q", prepared.Referenced.Stderr, want)
		}
		if len(prepared.Recovery) != 1 || len(prepared.Recovery[0].ChangeSelection) == 0 {
			t.Fatal("missing frozen selection")
		}
		published, err := session.PublishSelectionAt(f.ctx, handles[i], prepared.Recovery[0].ChangeSelection)
		if err != nil || published != handles[i] {
			t.Fatalf("publication replaced reserved reference: %q %v", published, err)
		}
		remainder, diag, status := f.run(t, "mread "+handles[i]+" --stdout --max-tokens 15500")
		if status != 0 || diag != "" || output.Stdout+remainder != full {
			t.Fatalf("reserved reference does not recover captured read: stdout=%q stderr=%q status=%d", remainder, diag, status)
		}
	}
}

type faultChangeDelivery struct {
	command.PendingDelivery
	prepareErr error
	fallbacks  int
}

func (p *faultChangeDelivery) Prepare(refs []string) (command.PreparedDelivery, error) {
	if p.prepareErr != nil {
		return command.PreparedDelivery{}, p.prepareErr
	}
	return p.PendingDelivery.Prepare(refs)
}

func (p *faultChangeDelivery) RecoveryFailed(err error) command.Output {
	p.fallbacks++
	return p.PendingDelivery.(command.RecoveryFailure).RecoveryFailed(err)
}

func TestMChangesDeliveryWorkerPrepublicationFailureAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			f, id, want := deliveryMutationFixture(t)
			t.Setenv(codexThreadIDEnvironment, f.thread)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var plan *faultChangeDelivery
			calls := 0
			commands := testCommands()
			position := slices.IndexFunc(commands, func(c command.Contribution) bool { return c.Name == "mchanges" })
			if position < 0 {
				t.Fatal("missing mchanges contribution")
			}
			commands[position].Run = func(ctx context.Context, request command.Request) (command.Output, error) {
				calls++
				output, err := f.store.changes.Execute(ctx, request)
				if err != nil {
					return output, err
				}
				if output.Delivery == nil {
					t.Fatalf("no completed mutation delivery: %+v", output)
				}
				plan = &faultChangeDelivery{PendingDelivery: output.Delivery, prepareErr: errors.New("prepare fault")}
				output.Delivery = plan
				if canceled {
					cancel()
				}
				return output, nil
			}
			var out, diag bytes.Buffer
			handled, status := runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), commands, ctx, f.registry.frontends["mchanges"],
				[]string{"revert", "--workspace", f.workspace, id, "--max-tokens", "12"}, nil, &out, &diag)
			if !handled || calls != 1 || plan == nil {
				t.Fatalf("worker did not execute exactly once: handled=%t calls=%d", handled, calls)
			}
			if canceled {
				if status != 1 || out.Len() != 0 || diag.String() != "mchanges: execute tool: context canceled\n" || plan.fallbacks != 0 {
					t.Fatalf("cancellation lost precedence: status=%d stdout=%q stderr=%q fallbacks=%d", status, out.String(), diag.String(), plan.fallbacks)
				}
			} else if status != 0 || out.String() != want || diag.String() != "mchanges: report not paged: prepare fault\n" || plan.fallbacks != 1 {
				t.Fatalf("completed mutation fallback: status=%d stdout=%q stderr=%q fallbacks=%d", status, out.String(), diag.String(), plan.fallbacks)
			}
			for i := range 20 {
				path := filepath.Join(f.workspace, fmt.Sprintf("change-%02d.txt", i))
				if got := readTestFile(t, path); got != "before\n" {
					t.Fatalf("completed mutation missing: %s=%q", path, got)
				}
			}
		})
	}
}
