package router

import (
	"errors"
	"os"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/orchestrate"
)

type orchestrateRunPicker struct {
	batches []orchestrate.Batch
}

func (u *appServerUI) orchestrationOwner() *appServerUI {
	if u.navigation != nil {
		return u.navigation.owner
	}
	return u
}

func (u *appServerUI) openOrchestratePicker() {
	owner := u.orchestrationOwner()
	if owner.proxy == nil || owner.proxy.orchestration == nil || owner.proxy.orchestration.store == nil {
		u.setNotice("Orchestration is unavailable in this session", true)
		return
	}
	if u.thread == "" || u.restoring != nil || u.replacement.pending() || owner.orchestrateClosing {
		u.setNotice("Wait for the session to be ready", false)
		return
	}
	u.takeDraft()
	u.cancelPickerScan()
	p := &u.picker
	run := new(orchestrateRunPicker)
	p.modal, p.orchestration, p.open, p.loading = "orchestrate", run, true, true
	p.selected, p.top, p.target, p.choices, p.problem = 0, 0, composerTarget{}, nil, ""
	store, workspace, main := owner.proxy.orchestration.store, owner.session.cwd, owner.thread
	owner.orchestrateWork(func() func() {
		batches, err := store.Snapshot(workspace, main)
		return func() {
			if p.orchestration != run || p.modal != "orchestrate" || owner.orchestrateClosing {
				return
			}
			p.loading = false
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				p.problem = "Read orchestration: " + err.Error()
			} else {
				run.batches = batches
				u.refreshOrchestratePicker()
			}
			u.dirty = true
		}
	})
}

// Retained task identity admits rows. Confirmed subscribed state supplies
// current lifecycle and metrics; a historical thread ID alone cannot select a view.
func (u *appServerUI) refreshOrchestratePicker() {
	p, owner := &u.picker, u.orchestrationOwner()
	if p.loading || p.problem != "" || p.orchestration == nil {
		return
	}
	p.choices = nil
	if len(p.orchestration.batches) == 0 {
		p.problem = "No batches · /orchestrate ISSUES starts a run"
		return
	}
	p.choices = append(p.choices, composerChoice{name: owner.thread, display: "main", description: u.orchestratePickerStatus(owner, owner.status)})
	for _, batch := range p.orchestration.batches {
		var view *appServerUI
		state := orchestrateBatchState(batch)
		if n := owner.navigation; n != nil {
			for _, retained := range n.retained {
				if retained.TaskName == batch.TaskName && state == "running" && orchestrateBatchState(retained) == "running" {
					state = orchestrateRestoredBatchState(batch)
					break
				}
			}
			for thread, child := range owner.orchestrateThreads {
				b := child.batch
				if child.command.main == owner.thread && child.command.workspace == owner.session.cwd && b.TaskName == batch.TaskName {
					batch, view = b, n.views[thread]
					state = orchestrateBatchState(batch)
					break
				}
			}
		}
		branch := strings.TrimSpace(liveActivityMiddle(batch.Branch, 24))
		choice := composerChoice{display: batch.TaskName, description: state + " · " + branch}
		if batch.TaskName == "main" {
			choice.display = "main batch"
		}
		if view != nil {
			choice.name = view.thread
			choice.description = u.orchestratePickerStatus(view, view.status+" · "+branch)
		} else if batch.Launch != nil && batch.Launch.ThreadID != "" {
			choice.description += " · not subscribed"
		}
		p.choices = append(p.choices, choice)
	}
	p.selected = min(p.selected, len(p.choices)-1)
}

func (u *appServerUI) orchestratePickerStatus(view *appServerUI, status string) string {
	parts := nativeRosterMetricParts(view.agents, *view.session.agent("/root"), u.now())
	for _, index := range []int{2, 6, 7, 8} {
		if metric := strings.TrimSpace(ansi.Strip(parts[index])); metric != "" {
			status += " · " + metric
		}
	}
	return status
}

func (u *appServerUI) orchestratePickerKey(key string) bool {
	p := &u.picker
	switch key {
	case "\x1b[200~", "\x1b[201~":
		return false
	case "\x1b", "\x03":
		p.modal, p.open, p.choices, p.orchestration = "", false, nil, nil
	case "\x1b[A", "\x1bOA", "\x10", "\x1b[B", "\x1bOB", "\x0e", "\t":
		if n := len(p.choices); n > 0 {
			step := 1
			if key == "\x1b[A" || key == "\x1bOA" || key == "\x10" {
				step = -1
			}
			p.selected = (p.selected + step + n) % n
		}
	case "\x1b[5~":
		p.selected = max(0, p.selected-8)
	case "\x1b[6~":
		p.selected = max(0, min(len(p.choices)-1, p.selected+8))
	case "\x1b[H", "\x1bOH":
		p.selected = 0
	case "\x1b[F", "\x1bOF":
		p.selected = max(0, len(p.choices)-1)
	case "\r":
		u.refreshOrchestratePicker()
		if p.loading || len(p.choices) == 0 {
			return true
		}
		thread := p.choices[p.selected].name
		if thread == "" {
			u.setNotice("This batch is not subscribed · launch prepared work or resume its retained thread", false)
			return true
		}
		if thread != u.orchestrationOwner().thread || u.navigation != nil {
			if !u.switchOrchestratedThread(thread) {
				u.setNotice("Orchestration thread is unavailable", true)
				return true
			}
		}
		p.modal, p.open, p.choices, p.orchestration = "", false, nil, nil
	}
	return true
}
