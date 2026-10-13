package router

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// Tool descriptions and parameter help belong to MCP metadata.
const orchestrateCoordinatorInstructions = `Use tools.mcp__orchestrate__spawn_agent for isolated implementation batches. Main looks up evidence, holds overall intent, current state and issues, delegates work, and verifies and integrates results. Children own planning, documentation, implementation, validation and correction. Mekugi owns review dispatch and correction routing. For change requests, this workflow includes local commits and linear Git integration, subject to explicit user restrictions. Diagnosis-only assignments remain read-only.`

// The runtime projects this policy for proven run members, including fresh resume.
const orchestrateOwnershipInstructions = `Orchestration role ownership replaces incompatible global workflow rules, including rules in previously read or recovered guidance. Main owns lookup, overall intent, current state, issues, delegation and integration. Children own assigned planning, documentation, implementation, validation and correction, including model guidance and workflow documents. Mekugi owns review dispatch and correction routing; agents do not launch a separate review loop. Keep compatible safety, authorization, scope, quality and tool policies. Global instruction files remain unchanged.`

const orchestrateWorkflow = orchestrateCoordinatorInstructions + "\nIssues:\n"

// Provisional intent stays with its queue entry. It grants no run membership.
type orchestrateInput struct {
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
	ctx, cancel := context.WithCancel(u.ctx)
	u.orchestrateStarting = cancel
	u.orchestrateWork(func() func() {
		start := func() error {
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
			return proxy.orchestration.store.Start(ctx, workspace, thread)
		}
		err := start()
		return func() {
			u.orchestrateStarting = nil
			defer cancel()
			if err == nil {
				err = ctx.Err()
			}
			if err == nil && (u.orchestrateClosing || u.thread != thread || u.session.cwd != workspace) {
				err = context.Canceled
			}
			if err != nil {
				removed := false
				remove := func(part composerDraft) bool {
					match := part.orchestration == pending
					removed = removed || match
					return match
				}
				u.unsent = slices.DeleteFunc(u.unsent, remove)
				u.queued = slices.DeleteFunc(u.queued, remove)
				if removed {
					u.restoreDrafts(original)
				}
				u.setNotice("Start orchestration: "+err.Error(), true)
				return
			}
			if err := u.flushInput(); err != nil {
				u.setNotice("Submit orchestration: "+err.Error(), true)
			}
			u.dirty = true
		}
	})
}

const orchestrateChildInstructions = `Your coordinator is main. Main's journal is readable with agent:"main"; your journal root is independent.
Own planning, documentation, implementation, validation and correction for your assignment in this checkout. For change requests, local commits are part of delivery; keep Git history linear. Mekugi owns review dispatch and correction routing; Main verifies and integrates your result. Honor explicit user restrictions and read-only assignments.
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
