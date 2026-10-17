package sessioncontrol

// Source: apps/mekugi/internal/router/app_server_session_title.go:12:111@[b15e8d58]

// NameSave is semantic response context. The request ledger owns its wire ID.
type NameSave struct {
	Thread, Name string
	manual       bool
}

type nameState struct {
	queued string
	saving bool
}

// Names serializes metadata saves independently of Main's execution lifecycle.
// It retains no confirmed-name cache; host observations supply display facts.
type Names struct {
	beforeStart string
	threads     map[string]nameState
}

func (n *Names) Manual(thread, name string) {
	if thread == "" {
		n.beforeStart = name
		return
	}
	if n.threads == nil {
		n.threads = make(map[string]nameState)
	}
	state := n.threads[thread]
	state.queued = name
	n.threads[thread] = state
}

func (n *Names) Attach(thread string) string {
	name := n.beforeStart
	n.beforeStart = ""
	if name != "" {
		n.Manual(thread, name)
	}
	return name
}

func (n *Names) Pending(thread string) string {
	if thread == "" {
		return n.beforeStart
	}
	return n.threads[thread].queued
}

func (n *Names) Next(thread string) *NameSave {
	state := n.threads[thread]
	if state.saving || state.queued == "" {
		return nil
	}
	state.saving = true
	n.threads[thread] = state
	return &NameSave{Thread: thread, Name: state.queued, manual: true}
}

func (n *Names) AllowsGenerated(thread string) bool {
	state := n.threads[thread]
	return !state.saving && state.queued == ""
}

func (n *Names) Generated(thread, name string) *NameSave {
	if !n.AllowsGenerated(thread) {
		return nil
	}
	if n.threads == nil {
		n.threads = make(map[string]nameState)
	}
	n.threads[thread] = nameState{saving: true}
	return &NameSave{Thread: thread, Name: name}
}

// Complete settles an acknowledged or failed save. Its caller has consumed the
// request identity once. True means no newer manual name supersedes this result.
func (n *Names) Complete(save NameSave) bool {
	state := n.threads[save.Thread]
	state.saving = false
	if save.manual && state.queued == save.Name {
		state.queued = ""
	}
	if state.queued == "" {
		delete(n.threads, save.Thread)
		return true
	}
	n.threads[save.Thread] = state
	return false
}
