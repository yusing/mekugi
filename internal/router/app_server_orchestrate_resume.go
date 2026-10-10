package router

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/yusing/mekugi/internal/appserver"
)

type orchestrateResume struct {
	view  *appServerUI
	ready []func(error)
}

func (u *appServerUI) resumeOrchestratedBatch(name string, ready func(error)) {
	u = u.orchestrationOwner()
	n := u.ensureOrchestrationNavigation()
	for _, b := range n.retained {
		if b.TaskName != name || b.Launch == nil || b.Launch.ThreadID == "" {
			continue
		}
		thread := b.Launch.ThreadID
		if r := n.resumes[thread]; r != nil {
			r.ready = append(r.ready, ready)
			return
		}
		if n.views[thread] != nil {
			ready(nil)
			return
		}
		if n.resumes == nil {
			n.resumes = make(map[string]*orchestrateResume)
		}
		r := &orchestrateResume{ready: []func(error){ready}}
		n.resumes[thread] = r
		u.setNotice("Resuming batch "+name, false)
		workspace, main, store := u.session.cwd, u.thread, u.proxy.orchestration.store
		u.orchestrateWork(func() func() {
			batch, err := store.Resume(u.orchestrateStorageContext, workspace, main, name)
			return func() {
				if u.orchestrateClosing || n.closed {
					err = context.Canceled
				}
				if err == nil && (u.thread != main || u.session.cwd != workspace || batch.Launch.ThreadID != thread) {
					err = errors.New("retained orchestration identity changed")
				}
				if err != nil {
					n.finishResume(thread, err)
					return
				}
				c := &orchestrateCommand{ctx: u.ctx, workspace: workspace, main: main, reply: make(chan orchestrateResult, 1)}
				child := &orchestrateChild{command: c, batch: batch}
				if u.orchestrateThreads == nil {
					u.orchestrateThreads = make(map[string]*orchestrateChild)
				}
				u.orchestrateThreads[thread] = child
				err = u.addOrchestratedView(child, batch.Launch.ThreadResult, func() {
					v := n.views[thread]
					r.view = v
					v.awaitingTurn = false
					v.resumeThread = thread
					v.restoring = &appServerActivityRestore{}
					// Invocation model overrides belong to Main. Restore the batch's
					// own applied settings through the shared resume owner.
					v.resumeConfig = maps.Clone(v.resumeConfig)
					delete(v.resumeConfig, "model")
					delete(v.resumeConfig, "model_reasoning_effort")
					delete(v.resumeConfig, "service_tier")
					u.session.roots[thread] = "/orchestrate/" + thread
					if err := v.requestResume(thread); err != nil {
						n.finishResume(thread, err)
					}
				})
				if err != nil {
					n.finishResume(thread, err)
				}
			}
		})
		return
	}
	ready(errors.New("target has no confirmed retained batch thread"))
}

// Check host identity before the ordinary controller consumes settings/history.
func (n *orchestrateNavigation) resumeResponse(v *appServerUI, m appserver.Message) bool {
	r := n.resumes[v.thread]
	method := v.requests[string(m.ID)]
	if r != nil && m.Error != nil && v.settings.restoreEffort && (method == "thread/settings/update" || method == "turn/settings/update") {
		delete(v.requests, string(m.ID))
		n.finishResume(v.thread, fmt.Errorf("restore default reasoning: %s", m.Error.Message))
		return true
	}
	if r == nil || method != "resume/settings" && method != "thread/resume" {
		return false
	}
	var result struct {
		Thread appServerThreadInfo `json:"thread"`
	}
	var err error
	if m.Error != nil {
		err = fmt.Errorf("%s: %s", method, m.Error.Message)
	} else {
		err = json.Unmarshal(m.Result, &result)
		b := n.owner.orchestrateThreads[v.thread].batch
		if err == nil && (result.Thread.Cwd != b.Cwd || result.Thread.ParentThreadID != "") {
			err = errors.New("host returned a different retained batch identity or cwd")
		}
	}
	if err != nil {
		delete(v.requests, string(m.ID))
		n.finishResume(v.thread, err)
		return true
	}
	if method == "thread/resume" {
		v.restoring = nil
	}
	return false
}

func (n *orchestrateNavigation) completeResumes() {
	for thread, r := range n.resumes {
		v := r.view
		if v == nil || v.restoring != nil || v.settings.pending() || v.settings.restoreEffort {
			continue
		}
		child := n.owner.orchestrateThreads[thread]
		child.turn = v.turn // Only a current host snapshot can restore a live turn.
		n.owner.session.registerThread(v.session.threads[thread])
		n.finishResume(thread, nil)
	}
}

func (n *orchestrateNavigation) finishResume(thread string, err error) {
	r := n.resumes[thread]
	if r == nil {
		return
	}
	delete(n.resumes, thread)
	if err == nil {
		name := n.owner.orchestrateThreads[thread].batch.TaskName
		if n.owner.notice == "Resuming batch "+name {
			n.owner.setNotice("Batch "+name+" resumed", false)
		}
	}
	if err != nil {
		n.owner.setNotice("Orchestration resume: "+err.Error(), true)
		if v := n.views[thread]; v != nil {
			v.closeOrchestratedView()
			delete(n.views, thread)
			n.order = slices.DeleteFunc(n.order, func(id string) bool { return id == thread })
		}
		delete(n.owner.orchestrateThreads, thread)
	}
	for _, ready := range r.ready {
		ready(err)
	}
}
