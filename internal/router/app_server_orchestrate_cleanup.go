package router

import (
	"context"
	"errors"
	"slices"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func (u *appServerUI) orchestrateCheckoutUnavailable() bool {
	if u.navigation != nil {
		if child := u.navigation.owner.orchestrateThreads[u.thread]; child != nil {
			return child.batch.State == "removing" || child.batch.State == "removed" || child.batch.State == "integrating"
		}
	}
	return false
}

func (p *mekugiProxy) orchestrateCleanupProof(ctx context.Context, workspace, main, name string, publishIntent func() error) (proof orchestrate.Integration, err error) {
	err = p.journals.transactionWithTiming(ctx, p.replayStore, workspace, main, false, func(j *threadJournal, exists bool) error {
		run := j.Orchestration
		if !exists || run == nil || run.Main != main || run.Workspace != workspace || run.Directory != p.orchestration.store.Directory || !j.IdentityKnown || j.IdentityConflicted || j.Parent != "" || j.Author != "/root" {
			return errors.New("cleanup requires the retained run coordinator")
		}
		batches, err := p.orchestration.store.Snapshot(workspace, main)
		if err != nil {
			return err
		}
		index := slices.IndexFunc(batches, func(b orchestrate.Batch) bool { return b.TaskName == name })
		if index < 0 || batches[index].Launch == nil || batches[index].Integration == nil {
			return errors.New("cleanup requires a confirmed integrated batch")
		}
		b := batches[index]
		found := false
		for _, item := range j.Items {
			if item.Agent == "/root/"+name {
				if item.State != "accepted" || item.Integration == nil || *item.Integration != *b.Integration {
					return errors.New("cleanup requires the current accepted journal task and tips")
				}
				found = true
			}
		}
		if !found {
			return errors.New("cleanup requires Main's accepted bound task")
		}
		journals, failures, err := p.journals.relatedJournals(p.replayStore.scoped(ctx), workspace, main)
		if err != nil {
			return err
		}
		if journals[b.Launch.ThreadID].LifecycleState != "done" {
			return errors.New("cleanup requires confirmed child completion")
		}
		for thread, child := range journals {
			if slices.Contains(journalAncestry(journals, thread), b.Launch.ThreadID) && (failures[thread] != nil || child.LifecycleState != "done") {
				return errors.New("cleanup requires settled child and native descendant lifecycles")
			}
		}
		proof = *b.Integration
		if publishIntent != nil {
			if err := publishIntent(); err != nil {
				return err
			}
		}
		return errJournalUnchanged
	})
	return proof, err
}

func (u *appServerUI) cleanOrchestratedChild(c *orchestrateCommand) {
	for _, child := range u.orchestrateThreads {
		if child.command.main != c.main || child.command.workspace != c.workspace || c.target != child.batch.TaskName && c.target != "/root/"+child.batch.TaskName {
			continue
		}
		if u.navigation == nil {
			c.reply <- orchestrateResult{err: errors.New("cleanup requires a subscribed child")}
			return
		}
		v := u.navigation.views[child.batch.Launch.ThreadID]
		if child.removing || child.turn != "" || len(child.followups) != 0 || v == nil || v.busy() || len(v.activeChildren) != 0 || v.reset.active() || v.questionCount() != 0 || len(v.approvals.pending) != 0 || len(v.btwRequests) != 0 || v.btw != nil && (v.btw.busy || v.btw.starting || v.btw.interrupting) || v.draft != "" || len(v.images)+len(v.files)+len(v.skills)+len(v.selections) != 0 || v.paste || v.shell.paste {
			c.reply <- orchestrateResult{err: errors.New("cleanup requires an idle subscribed child with no pending input or descendants")}
			return
		}
		for _, request := range u.orchestrateRequests {
			if request.child == child {
				c.reply <- orchestrateResult{err: errors.New("cleanup requires settled child host requests")}
				return
			}
		}
		previous := child.batch
		child.removing = true
		child.batch.State = "removing" // Reserve the live target before storage waits.
		u.orchestrateWork(func() func() {
			proof, err := u.proxy.orchestrateCleanupProof(c.ctx, c.workspace, c.main, previous.TaskName, nil)
			batch := previous
			if err == nil {
				var result orchestrate.Batch
				result, err = u.proxy.orchestration.store.Cleanup(c.ctx, c.workspace, c.main, previous.TaskName, proof, func(publish func() error) error {
					_, err := u.proxy.orchestrateCleanupProof(c.ctx, c.workspace, c.main, previous.TaskName, publish)
					return err
				})
				if result.TaskName != "" {
					batch = result
				}
			}
			return func() {
				child.removing = false
				child.batch = batch
				if batch.State == "removed" {
					v.setNotice("Batch checkout removed; its transcript remains available", false)
				}
				c.reply <- orchestrateResult{batch: batch, err: err}
			}
		})
		return
	}
	c.reply <- orchestrateResult{err: errors.New("cleanup target is not a subscribed batch in this run")}
}
