package launch

import (
	"context"

	"errors"

	"strings"

	"testing"

	"github.com/yusing/mekugi/contracts/command"

	"github.com/yusing/mekugi/libraries/diffmodel"

	"github.com/yusing/mekugi/libraries/legacycall"

	"github.com/yusing/mekugi/modules/changehistory/changes"
)

func putTestChange(t *testing.T, ctx context.Context, store *mekugiReplayStore, workspace, correlation string, files ...diffmodel.ReviewFile) string {
	t.Helper()
	id, err := store.changes.Reserve(ctx, store.scoped(ctx).changeScope(), workspace, "stock-thread", correlation)
	if err != nil {
		t.Fatal(err)
	}
	history := legacycall.History{ChangeID: id, CorrelationID: correlation, ReviewFiles: retainedReviewFiles(files)}
	if err := store.put(ctx, workspace, map[string]legacycall.History{correlation: history}); err != nil {
		t.Fatal(err)
	}
	return id
}

func mutateTestChanges(t *testing.T, ctx context.Context, store *mekugiReplayStore, workspace string, arguments ...string) (string, int) {
	t.Helper()
	options, err := testChanges().Parse(arguments, workspace)
	if err != nil {
		t.Fatal(err)
	}
	output, status, err := mutateChangeFixture(store, ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	return output, status
}

func mutateChangeFixture(store *mekugiReplayStore, ctx context.Context, query changes.Query) (string, int, error) {
	args := append([]string{query.View, "--workspace", query.Workspace, "--max-tokens", "15500"}, query.IDs...)
	if len(query.Paths) > 0 {
		args = append(append(args, "--"), query.Paths...)
	}
	output, err := store.changes.Execute(ctx, command.Request{Args: args, Scope: retainedScope(ctx, store.directory)})
	if err != nil {
		return output.Stdout, output.ExitCode, err
	}
	if output.Stderr != "" {
		return output.Stdout, output.ExitCode, errors.New(strings.TrimSpace(output.Stderr))
	}
	return output.Stdout, output.ExitCode, nil
}
