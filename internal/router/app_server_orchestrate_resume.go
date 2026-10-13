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
	authenticated bool
	view          *appServerUI
	ready         []func(error)
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
			if child := u.orchestrateThreads[thread]; implementationWorkflow(child) {
				u.reconcileCorrectionLifecycle(child)
			}
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
				raw, _ := json.Marshal(map[string]any{"thread": appServerThreadInfo{ID: thread, Cwd: batch.Cwd}})
				err = u.addOrchestratedView(child, raw, func() {
					v := n.views[thread]
					r.view = v
					v.awaitingTurn = false
					v.resumeThread = thread
					v.restoring = &appServerActivityRestore{}
					if batch.Archive != nil {
						// Archived threads cannot resume. Read retained history without
						// unarchiving, restoring processes, or writing host settings.
						if _, err := v.requestAs("thread/read", "archive/history", map[string]any{"threadId": thread, "includeTurns": true}); err != nil {
							n.finishResume(thread, err)
						}
						return
					}
					// Invocation model overrides belong to Main. Restore the batch's
					// own applied settings through the shared resume owner.
					v.resumeConfig = maps.Clone(v.resumeConfig)
					delete(v.resumeConfig, "model")
					delete(v.resumeConfig, "model_reasoning_effort")
					delete(v.resumeConfig, "service_tier")
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
	if r != nil && m.Error != nil && (method == "thread/settings/update" || method == "turn/settings/update") {
		delete(v.requests, string(m.ID))
		n.finishResume(v.thread, fmt.Errorf("restore batch settings: %s", m.Error.Message))
		return true
	}
	if r == nil || method != "resume/settings" && method != "thread/resume" && method != "archive/history" {
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
		if err == nil && (result.Thread.ID != v.thread || result.Thread.Cwd != b.Cwd || result.Thread.ParentThreadID != "") {
			err = errors.New("host returned a different retained batch identity or cwd")
		}
	}
	if err != nil {
		delete(v.requests, string(m.ID))
		n.finishResume(v.thread, err)
		return true
	}
	if method == "archive/history" {
		delete(v.requests, string(m.ID))
		v.title = result.Thread.Name
		v.session.registerThread(result.Thread)
		v.restoreContextUsage(v.session.agent("/root"), result.Thread)
		v.restoreUsage(result.Thread)
		var retained nativeAppliedSettings
		if v.panes != nil {
			if state, err := v.panes.read(result.Thread.Cwd, v.thread); err != nil {
				n.finishResume(v.thread, err)
				return true
			} else {
				retained = state.Settings
			}
		}
		settings := retained.config()
		if settings == nil {
			settings = make(map[string]any)
		}
		maps.Copy(settings, readResumeSettings(result.Thread, retained.Observed))
		v.model, _ = settings["model"].(string)
		v.reasoningEffort, _ = settings["model_reasoning_effort"].(string)
		v.serviceTier, _ = settings["service_tier"].(string)
		for _, sink := range []*nativeJournalSink{v.journal, v.unscopedJournal} {
			if err := v.proxy.journals.restoreNative(v.ctx, v.proxy.replayStore, sink); err != nil {
				n.finishResume(v.thread, err)
				return true
			}
		}
		if err := v.restorePaneContent(result.Thread); err != nil {
			n.finishResume(v.thread, err)
		}
		return true
	}
	if method == "thread/resume" {
		r.authenticated = true
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
		n.owner.releaseRetired(thread)
		child := n.owner.orchestrateThreads[thread]
		if implementationWorkflow(child) {
			child.workflowDeferred = false
			n.owner.reconcileWorkflow(child)
		}
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
