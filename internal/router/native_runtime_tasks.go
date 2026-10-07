package router

import (
	"slices"
	"strings"

	"github.com/yusing/mekugi/internal/session"
)

func runtimeTaskLane(id string) string { return "/root/" + id }
func runtimeTaskTerminal(status string) bool {
	return status == "completed" || status == "failed" || status == "stopped" || status == "killed"
}

// Task IDs are native presentation identities, not invented agent principals.
// Root completion cannot complete still-running background tasks.
func (u *appServerUI) runtimeTask(e session.Event) {
	if e.Task == nil || e.Task.ID == "" {
		return
	}
	r := u.runtime
	if r.tasks == nil {
		r.tasks = make(map[string]session.Task)
	}
	t := *e.Task
	old, exists := r.tasks[t.ID]
	if !exists {
		if len(r.tasks) >= 256 {
			retired := slices.IndexFunc(r.taskOrder, func(id string) bool { return runtimeTaskTerminal(r.tasks[id].Status) })
			if retired < 0 {
				u.setNotice("Native task display limit reached", false)
				return
			}
			delete(r.tasks, r.taskOrder[retired])
			r.taskOrder = slices.Delete(r.taskOrder, retired, retired+1)
		}
		r.taskOrder = append(r.taskOrder, t.ID)
	}
	if t.ToolID == "" {
		t.ToolID = old.ToolID
	}
	if t.Kind == "" {
		t.Kind = old.Kind
	}
	if t.Role == "" {
		t.Role = old.Role
	}
	if t.Description == "" {
		t.Description = old.Description
	}
	if t.Status == "" {
		t.Status = old.Status
	}
	if t.Summary == "" && e.Role != "task_started" {
		t.Summary = old.Summary
	}
	t.Ambient = t.Ambient || old.Ambient
	if e.Role == "task_progress" {
		t.Status = old.Status
	}
	r.tasks[t.ID] = t
	if t.Kind == "local_bash" && runtimeTaskTerminal(t.Status) {
		u.runtimeFinishCommand(t.ToolID, t.Status == "failed")
	}
	if !t.Ambient {
		caller := "task/" + t.ID
		if t.Kind == "local_bash" {
			caller = ""
		}
		text := strings.TrimSpace(t.Description)
		if t.Status != "" {
			text += " · " + t.Status
		}
		if t.Summary != "" && t.Summary != t.Description {
			text += "\n" + t.Summary
		}
		u.runtimeEntry(session.Event{Kind: "task", ID: "task/" + t.ID, Caller: caller, Text: text})
	}
	u.runtimeRoster()
}

func (u *appServerUI) runtimeRoster() {
	r := u.runtime
	root := activityPaneAgent{Name: "/root", Responding: r.busy}
	if r.usage != nil {
		for _, model := range r.usage.Models {
			root.UsagePartial = root.UsagePartial || model.Input == nil || model.Output == nil || model.CacheRead == nil || model.CacheWrite == nil
			if model.Input != nil {
				root.InputTokens += *model.Input
			}
			if model.Output != nil {
				root.OutputTokens += *model.Output
			}
			if model.CacheRead != nil {
				root.InputTokens += *model.CacheRead
			}
			if model.CacheWrite != nil {
				root.InputTokens += *model.CacheWrite
			}
		}
		root.TokensKnown = len(r.usage.Models) > 0 && !root.UsagePartial
	}
	agents := []activityPaneAgent{root}
	for _, id := range r.taskOrder {
		task := r.tasks[id]
		if task.Ambient || task.Kind == "local_bash" {
			continue
		}
		agents = append(agents, activityPaneAgent{Name: runtimeTaskLane(id), Role: task.Role, Responding: task.Status == "running", Final: task.Status == "completed", State: new(task.Status)})
	}
	u.agents.apply(activityPaneEvent{Kind: "agents", Agents: agents})
}

// A task can be selected through its roster row or its shared disclosure. Shell
// jobs stay out of the agent roster without losing their native stop control.
func (u *appServerUI) runtimeSelectedTask() string {
	if u.runtime == nil {
		return ""
	}
	if _, ok := u.runtime.client.(session.TaskClient); !ok {
		return ""
	}
	call := ""
	if dialog := u.shell.output; dialog != nil {
		for _, entry := range dialog.view.entries {
			if entry.Seq == dialog.pages[dialog.page].Source {
				call = entry.CallID
				break
			}
		}
	}
	for _, id := range u.runtime.taskOrder {
		task := u.runtime.tasks[id]
		selected := u.shell.output == nil && u.shell.focus == 3 && runtimeTaskLane(id) == u.agents.selected
		if selected || call != "" && (call == "task/"+id || call == task.ToolID) {
			if !runtimeTaskTerminal(task.Status) {
				return id
			}
		}
	}
	return ""
}

func (u *appServerUI) runtimeCanStopTask() bool {
	return u.runtimeSelectedTask() != ""
}

func (u *appServerUI) runtimeStopTask() error {
	r := u.runtime
	client, ok := r.client.(session.TaskClient)
	if !ok {
		return nil
	}
	id := u.runtimeSelectedTask()
	if id == "" || r.stoppingTasks[id] {
		return nil
	}
	if err := client.StopTask(u.ctx, id); err != nil {
		u.setNotice("Native task stop failed: "+err.Error(), true)
		return nil
	}
	if r.stoppingTasks == nil {
		r.stoppingTasks = make(map[string]bool)
	}
	r.stoppingTasks[id] = true
	u.setNotice("Requesting native task stop…", false)
	return nil
}
