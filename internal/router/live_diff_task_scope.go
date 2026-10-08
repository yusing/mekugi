package router

import (
	"maps"
	"strings"

	"github.com/yusing/mekugi/internal/ui/diffview"
)

type liveDiffTaskScope byte

const (
	liveDiffSubslice liveDiffTaskScope = iota
	liveDiffSlice
	liveDiffAll
)

// Prefer a working leaf. Once work stops, keep the last completed task's diff.
// Mounted tasks belong to their own callers, not the current owned task.
func liveDiffCurrentTask(items []journalItem) *journalItem {
	var current *journalItem
	priority := func(state string) int {
		switch state {
		case "working":
			return 0
		case "blocked":
			return 1
		case "done":
			return 2
		}
		return 3
	}
	for i := range items {
		item := &items[i]
		if item.Kind != "task" || item.Path == "" || strings.Contains(item.Path, "/@") || item.SupersededBy != "" || priority(item.State) == 3 {
			continue
		}
		if current == nil || priority(item.State) < priority(current.State) || priority(item.State) == priority(current.State) &&
			(strings.HasPrefix(item.Path, current.Path+"/") || !strings.HasPrefix(current.Path, item.Path+"/") && item.Updated > current.Updated) {
			current = item
		}
	}
	return current
}

func (c *liveDiffTerminalController) updateTaskScope(j *threadJournal) {
	c.taskJournal = j
	var paths map[string]string
	label := ""
	if j != nil {
		if task := liveDiffCurrentTask(j.Items); task != nil {
			path := task.Path
			switch c.taskScope {
			case liveDiffSubslice:
				label = "subslice " + path
			case liveDiffSlice:
				path = "/" + strings.Split(strings.TrimPrefix(path, "/"), "/")[0]
				label = "slice " + path
			case liveDiffAll:
				label = "all"
			}
			if c.taskScope != liveDiffAll {
				paths = map[string]string{j.Author: path}
				for _, item := range j.Items {
					if item.Agent != "" && (item.Path == path || strings.HasPrefix(item.Path, path+"/")) {
						paths[item.Agent] = ""
					}
				}
			}
		}
	}
	if maps.Equal(paths, c.view.TaskPaths) && label == c.navigation.Scope {
		return
	}
	c.view.TaskPaths, c.view.Visible = paths, nil
	c.view.RefreshVisible()
	c.navigation.Scope = label
	c.navigation.Changes.Target = diffview.ChangeTarget{}
	c.refreshChanges()
	// Do not leave a hidden file selected when the task or scope changes.
	if c.view.Selected < len(c.view.Files) && len(c.view.Visible[c.view.Files[c.view.Selected].Key()].Chunks) > 0 {
		c.dirty = true
		return
	}
	for i, file := range c.view.Files {
		if len(c.view.Visible[file.Key()].Chunks) > 0 {
			c.view.Open(i)
			break
		}
	}
	c.dirty = true
}

func (c *liveDiffTerminalController) cycleTaskScope() {
	c.taskScope = (c.taskScope + 1) % 3
	c.updateTaskScope(c.taskJournal)
}
