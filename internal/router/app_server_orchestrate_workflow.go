package router

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// Tool descriptions and parameter help belong to MCP metadata.
const orchestrateCoordinatorInstructions = `Use tools.mcp__orchestrate__spawn_agent for isolated implementation batches. For change requests, this workflow includes local commits and linear Git integration, subject to explicit user restrictions. Diagnosis-only assignments remain read-only.`

// The runtime projects this policy for proven run members, including fresh resume.
const orchestrateOwnershipInstructions = `Orchestration role ownership replaces incompatible global workflow rules, including rules in previously read or recovered guidance. Main owns lookup, overall intent, current state, issues, delegation and integration. Main directly corrects its own scope, delegation and instruction mistakes instead of assigning an audit or asking the user to repeat an explicit correction. Keep separately requested work in separate assignments. Children own assigned planning, documentation, implementation, validation and correction, including model guidance and workflow documents. Mekugi owns review dispatch and correction routing; agents do not launch a separate review loop. Keep compatible safety, authorization, scope, quality and tool policies. Global instruction files remain unchanged.`

const orchestrateWorkflow = orchestrateCoordinatorInstructions + "\nIssues:\n"

// Provisional intent stays with its queue entry. It grants no run membership.
type orchestrateInput struct {
	ready    bool
	owned    bool
	run      journalRun
	original composerDraft
}

func (u *appServerUI) submitOrchestrate() {
	prefix := strings.Index(u.draft, "/orchestrate") + len("/orchestrate")
	if strings.TrimSpace(u.draft[prefix:]) == "" {
		u.openOrchestratePicker()
		return
	}
	if u.proxy == nil || u.proxy.orchestration == nil || u.proxy.orchestration.store == nil || u.proxy.journals == nil {
		u.setNotice("Orchestration is unavailable in this session", true)
		return
	}
	if u.thread == "" || u.restoring != nil || u.replacement.pending() || u.orchestrateStarting != nil {
		u.setNotice("Wait for the session to be ready", false)
		return
	}
	info, known := u.session.threads[u.thread]
	if !known || info.ParentThreadID != "" || u.orchestrationOwner() != u {
		u.setNotice("Start orchestration from Main", true)
		return
	}
	original := u.draftSnapshot()
	u.deleteDraftRange(0, prefix)
	input := joinDrafts(composerDraft{text: orchestrateWorkflow}, u.takeDraft())
	proxy, workspace, thread := u.proxy, u.session.cwd, u.thread
	pending := &orchestrateInput{run: journalRun{Directory: proxy.orchestration.store.Directory, Workspace: workspace, Main: thread}, original: original}
	input.orchestration = pending
	if u.waitingQuestion() {
		u.queued = append(u.queued, input)
	} else {
		u.unsent = append(u.unsent, input)
	}
	u.view.follow()
	if err := u.flushInput(); err != nil {
		u.setNotice("Submit orchestration: "+err.Error(), true)
	}
}

// Journal and replay locks stay on the worker. The queue continues to own input
// until persistence completes; cancellation restores it through the composer.
func (u *appServerUI) admitOrchestrateInput(pending *orchestrateInput) {
	proxy, run := u.proxy, pending.run
	ctx, cancel := context.WithCancel(u.ctx)
	u.orchestrateStarting = cancel
	u.orchestrateWork(func() func() {
		created := false
		workspace, thread := run.Workspace, run.Main
		admit := func() error {
			if proxy.replayStore != nil {
				var release func()
				var err error
				ctx, release, err = proxy.replayStore.beginSession(ctx, thread, "")
				if err != nil {
					return err
				}
				defer release()
			}
			// Never overwrite retained native/child identity to admit a command.
			if err := proxy.journals.transaction(ctx, proxy.replayStore, workspace, thread, func(j *threadJournal, exists bool) error {
				if exists && (j.IdentityConflicted || j.Parent != "" || j.Author != "/root" || j.Orchestration != nil && j.Orchestration.Main != thread) {
					return errors.New("Start orchestration from Main")
				}
				return errJournalUnchanged
			}); err != nil {
				return err
			}
			if err := proxy.journals.initialize(ctx, proxy.replayStore, workspace, thread, "/root", ""); err != nil {
				return err
			}
			if err := proxy.journals.bindIdentity(ctx, proxy.replayStore, workspace, thread, "", "/root", true); err != nil {
				return err
			}
			if err := orchestrateMain(ctx, proxy, workspace, thread, ""); err != nil {
				return err
			}
			if err := proxy.orchestration.store.Start(ctx, workspace, thread); err != nil {
				return err
			}
			var err error
			created, err = proxy.journals.bindRunOwned(ctx, proxy.replayStore, workspace, thread, run)
			return err
		}
		err := admit()
		return func() {
			queued := slices.ContainsFunc(slices.Concat(u.unsent, u.queued), func(part composerDraft) bool { return part.orchestration == pending })
			if err == nil {
				err = ctx.Err()
			}
			if err == nil && (!queued || u.orchestrateClosing || u.thread != run.Main || u.session.cwd != run.Workspace) {
				err = context.Canceled
			}
			finish := func(cleanupErr error) {
				cancel()
				u.orchestrateStarting = nil
				if err != nil || cleanupErr != nil {
					u.restoreDrafts(slices.Concat(u.unsent, u.queued)...)
					u.unsent, u.queued = nil, nil
					u.setNotice("Admit orchestration: "+errors.Join(err, cleanupErr).Error(), true)
				} else {
					pending.ready, pending.owned = true, created
					if flushErr := u.flushInput(); flushErr != nil {
						u.setNotice("Submit orchestration: "+flushErr.Error(), true)
					}
				}
				u.dirty = true
			}
			if err != nil && created {
				// Retain existing runs and any run already used by an authenticated prepare.
				u.orchestrateWork(func() func() {
					cleanupErr := proxy.journals.releaseUnusedRun(u.orchestrateStorageContext, proxy.replayStore, run)
					return func() { finish(cleanupErr) }
				})
			} else {
				finish(nil)
			}
		}
	})
}

const orchestrateChildInstructions = `Your coordinator is main. Main's journal is readable with agent:"main"; your journal root is independent.
For change requests, local commits are part of delivery; keep Git history linear. Honor explicit user restrictions and read-only assignments.
`

// relatedJournals has verified this root's durable run membership before rendering.
func orchestrateRecoveryInstructions(j threadJournal) string {
	if j.Orchestration == nil || j.Parent != "" || j.Author != "/root" {
		return ""
	}
	if j.Thread == j.Orchestration.Main {
		return "Orchestration tools\n" + orchestrateCoordinatorInstructions + "\n" + orchestrateOwnershipInstructions + "\n\n"
	}
	return "Orchestration tools\n" + orchestrateChildInstructions + orchestrateOwnershipInstructions + "\n"
}

// A definitive rejection releases only membership created by this submission.
// Keep dispatch paused until the cleanup is durable, including after Ctrl-C.
func (u *appServerUI) rejectOrchestrateInput(parts []composerDraft) {
	var runs []journalRun
	for _, part := range parts {
		if pending := part.orchestration; pending != nil && pending.owned {
			runs = append(runs, pending.run)
			pending.owned, pending.ready = false, false
		}
	}
	if len(runs) == 0 {
		return
	}
	u.orchestrateStarting = func() {}
	proxy := u.proxy
	u.orchestrateWork(func() func() {
		var err error
		for _, run := range runs {
			err = errors.Join(err, proxy.journals.releaseUnusedRun(u.orchestrateStorageContext, proxy.replayStore, run))
		}
		return func() {
			u.orchestrateStarting = nil
			if err != nil {
				u.setNotice("Release rejected orchestration: "+err.Error(), true)
			} else if err := u.flushInput(); err != nil {
				u.setNotice("Submit input: "+err.Error(), true)
			}
		}
	})
}
