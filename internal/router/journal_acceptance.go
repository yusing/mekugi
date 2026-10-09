package router

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/yusing/mekugi/internal/orchestrate"
)

// Checkout observation and run writes happen before taking journal/replay locks.
// A saved integration proof alone never changes the journal task's state.
func (p *mekugiProxy) prepareJournalAcceptance(ctx context.Context, workspace, thread, receipt string, mutations []journalMutation) ([]journalMutation, error) {
	if !slices.ContainsFunc(mutations, func(m journalMutation) bool { return m.State != nil && *m.State == "accepted" }) {
		return mutations, nil
	}
	var current threadJournal
	err := p.journals.transactionWithTiming(ctx, p.replayStore, workspace, thread, false, func(j *threadJournal, exists bool) error {
		if !exists {
			return errors.New("acceptance requires an existing journal")
		}
		current = j.clone()
		return errJournalUnchanged
	})
	if err != nil {
		return nil, err
	}
	if _, ok := current.Receipts[receipt]; receipt != "" && ok {
		return mutations, nil // The mutation owner checks the original digest.
	}
	run := current.Orchestration
	if run == nil || run.Main != thread || run.Workspace != workspace || !current.IdentityKnown || current.IdentityConflicted || current.Parent != "" || current.Author != "/root" {
		return nil, errors.New("only the orchestration coordinator can accept a batch")
	}
	store := &orchestrate.Store{Directory: run.Directory}
	batches, err := store.Snapshot(workspace, thread)
	if err != nil {
		return nil, err
	}
	mutations = slices.Clone(mutations)
	for i := range mutations {
		m := &mutations[i]
		if m.State == nil || *m.State != "accepted" {
			continue
		}
		index := current.treeIndex(m.P)
		if m.Op != "set" || index < 0 {
			return nil, errors.New("accept an existing bound batch task with set")
		}
		item := current.Items[index]
		if item.State == "accepted" {
			continue
		}
		batch := slices.IndexFunc(batches, func(b orchestrate.Batch) bool {
			return item.Agent == "/root/"+b.TaskName && b.Launch != nil && b.Launch.ThreadID != ""
		})
		if batch < 0 {
			return nil, fmt.Errorf("%s: acceptance requires a confirmed batch binding", item.Path)
		}
		proof, err := store.RecordIntegration(ctx, workspace, thread, batches[batch].TaskName, batches[batch].Launch.ThreadID)
		if err != nil {
			return nil, err
		}
		m.integration = &proof
	}
	return mutations, nil
}

// The transaction rechecks authority and exact proof after preflight persistence.
func validateJournalAcceptance(j threadJournal, before []journalItem) error {
	changed := func(item journalItem) bool {
		previous := slices.IndexFunc(before, func(old journalItem) bool { return old.Path == item.Path })
		return previous < 0 || before[previous].State != item.State || before[previous].Agent != item.Agent
	}
	if !slices.ContainsFunc(j.Items, func(item journalItem) bool {
		return changed(item) && journalStateCompleted(item.State) && (item.State == "accepted" || item.Agent != "")
	}) {
		return nil
	}
	var batches []orchestrate.Batch
	run := j.Orchestration
	main := run != nil && run.Main == j.Thread && run.Workspace == j.Workspace && j.IdentityKnown && !j.IdentityConflicted && j.Parent == "" && j.Author == "/root"
	if main {
		var err error
		batches, err = (&orchestrate.Store{Directory: run.Directory}).Snapshot(run.Workspace, run.Main)
		if err != nil {
			return err
		}
	}
	for _, item := range j.Items {
		if !changed(item) || !journalStateCompleted(item.State) {
			continue
		}
		batch := slices.IndexFunc(batches, func(b orchestrate.Batch) bool { return item.Agent == "/root/"+b.TaskName })
		if item.State == "accepted" {
			if !main || batch < 0 || batches[batch].Launch == nil || batches[batch].Integration == nil || item.Integration == nil || *batches[batch].Integration != *item.Integration {
				return fmt.Errorf("%s: accepted requires Main's confirmed batch and recorded integration", item.Path)
			}
		} else if batch >= 0 {
			return fmt.Errorf("%s: integrated batch tasks require accepted, not done", item.Path)
		}
	}
	return nil
}
