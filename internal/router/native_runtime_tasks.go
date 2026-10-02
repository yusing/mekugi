package router

import (
	"slices"

	"github.com/yusing/mekugi/internal/session"
)

func runtimeTaskLane(id string) string { return "native/task/" + id }
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
	if !t.Ambient {
		u.runtimeEntry(session.Event{Kind: "task", ID: "task/" + t.ID, Caller: "task/" + t.ID, Text: t.Description + "\n\n" + t.Status + "\n\n" + t.Summary})
	}
	var agents []activityPaneAgent
	for _, id := range r.taskOrder {
		task := r.tasks[id]
		if task.Ambient {
			continue
		}
		agents = append(agents, activityPaneAgent{Name: runtimeTaskLane(id), Role: task.Kind, Responding: task.Status == "running", Final: task.Status == "completed", State: new(task.Status)})
	}
	u.agents.apply(activityPaneEvent{Kind: "agents", Agents: agents})
}

func (u *appServerUI) runtimeCanStopTask() bool {
	if u.runtime == nil {
		return false
	}
	if _, ok := u.runtime.client.(session.TaskClient); !ok {
		return false
	}
	for _, id := range u.runtime.taskOrder {
		if runtimeTaskLane(id) == u.agents.selected && !runtimeTaskTerminal(u.runtime.tasks[id].Status) {
			return true
		}
	}
	return false
}

func (u *appServerUI) runtimeStopTask() error {
	r := u.runtime
	client, ok := r.client.(session.TaskClient)
	if !ok {
		return nil
	}
	for _, id := range r.taskOrder {
		if runtimeTaskLane(id) != u.agents.selected {
			continue
		}
		if runtimeTaskTerminal(r.tasks[id].Status) || r.stoppingTasks[id] {
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
	return nil
}
