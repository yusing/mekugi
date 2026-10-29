package retained

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type service struct{ history *history.Service }

func New(history *history.Service) (changes.Service, error) {
	if history == nil {
		return nil, errors.New("change history requires session history")
	}
	return &service{history: history}, nil
}
func (*service) Reserve(ctx context.Context, selected changes.Scope, workspace, thread, correlation string) (string, error) {
	return selectedStore(selected).reserveChange(ctx, workspace, thread, correlation)
}
func (*service) Publish(tx *history.Transaction, selected changes.Scope, workspace string, values map[string]changes.Publication) (updates []changes.Update, err error) {
	store := selectedStore(selected)
	store.transaction, store.updates = tx, &updates
	err = store.publishChanges(workspace, values)
	return
}
func (*service) Repair(ctx context.Context, selected changes.Scope, workspace string, values map[string]changes.Publication) (updates []changes.Update, err error) {
	store := selectedStore(selected)
	store.updates = &updates
	err = store.repairVisibleChangeCalls(ctx, workspace, values)
	return
}
func (*service) Inspect(selected changes.Scope, workspace string) (changes.Snapshot, error) {
	index, err := selectedStore(selected).readChangeIndex(workspace)
	return snapshot(index), err
}
func (*service) InspectVisible(selected changes.Scope, workspace string, threads map[string]bool) ([]changes.Snapshot, error) {
	indexes, err := selectedStore(selected).visibleIndexes(workspace, threads)
	if err != nil {
		return nil, err
	}
	result := make([]changes.Snapshot, 0, len(indexes))
	for _, index := range indexes {
		result = append(result, snapshot(index))
	}
	return result, nil
}
func (*service) Dependencies(selected changes.Scope, workspace string, ids []string, existingOnly bool) ([]string, error) {
	return selectedStore(selected).dependencies(workspace, ids, existingOnly)
}
func (*service) Parse(args []string, directory string) (changes.Query, error) {
	value, err := parseChangeRead(args, directory)
	return value.query(), err
}
func (*service) Read(ctx context.Context, selected changes.Scope, q changes.Query) (string, error) {
	return selectedStore(selected).readChanges(ctx, options(q))
}
func (*service) MutationPaths(ctx context.Context, selected changes.Scope, q changes.Query) ([]string, error) {
	return selectedStore(selected).changeMutationPaths(ctx, options(q))
}
func (o changeReadOptions) query() changes.Query {
	return changes.Query{Workspace: o.workspace, IDs: o.ids, Paths: o.paths, View: o.view, Mine: o.mine, MaxTokens: o.maxTokens}
}
func options(q changes.Query) changeReadOptions {
	return changeReadOptions{workspace: q.Workspace, cwd: q.Workspace, ids: q.IDs, paths: q.Paths, view: q.View, mine: q.Mine, maxTokens: q.MaxTokens}
}

func (service *service) Execute(ctx context.Context, request command.Request) (command.Output, error) {
	fail := func(err error) (command.Output, error) {
		return command.Output{Stderr: fmt.Sprintf("mchanges: %v\n", err), ExitCode: 1}, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fail(fmt.Errorf("resolve working directory: %w", err))
	}
	options, err := parseChangeRead(request.Args, cwd)
	if err != nil {
		return fail(err)
	}
	if request.Scope.Directory == "" {
		return fail(errors.New("change storage is unavailable"))
	}
	if info, err := os.Lstat(filepath.Join(request.Scope.Directory, "store.lock")); err != nil || !info.Mode().IsRegular() {
		return fail(errors.New("change storage is missing or invalid"))
	}
	store := selectedStore(changes.Scope{Store: service.history.Attach(request.Scope.Directory), Namespace: request.Scope.Namespace, Thread: request.Scope.Thread})
	if options.view == "revert" || options.view == "apply" {
		text, status, err := store.mutateChanges(ctx, options)
		if err != nil {
			return fail(err)
		}
		selected, err := selectReadPage(ctx, text, options.maxTokens)
		if err != nil {
			return command.Output{Stdout: text, Stderr: "mchanges: report not paged: " + err.Error() + "\n", ExitCode: status}, nil
		}
		execution := command.Output{Stdout: selected, ExitCode: status}
		if len(selected) < len(text) {
			execution.Delivery = &changeMutationDelivery{report: text, selected: selected, status: status}
			execution.ExitCode = 1
		}
		return execution, nil
	}
	text, snapshot, err := store.readChangeView(ctx, options)
	diagnostics := ""
	if err != nil {
		if _, partial := errors.AsType[*partialChangeReadError](err); !partial {
			return fail(err)
		}
		diagnostics = fmt.Sprintf("mchanges: %v\n", err)
	}
	selected, err := selectReadPage(ctx, text, options.maxTokens)
	if err != nil {
		return fail(err)
	}
	execution := command.Output{Stdout: selected, Stderr: diagnostics}
	if diagnostics != "" {
		execution.ExitCode = 1
		if len(selected) < len(text) {
			execution.OmittedOutput = &command.OmittedOutput{Stdout: text[len(selected):], StdoutKind: "rows"}
		}
	} else if len(selected) < len(text) {
		execution.ExitCode = 1
		execution.Delivery = &changeReadDelivery{snapshot: *snapshot, text: text, selected: selected}
	}
	return execution, nil
}
