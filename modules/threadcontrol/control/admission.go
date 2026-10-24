package control

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
)

func activeCount(tree *Tree) int {
	count := 0
	for _, member := range tree.Members {
		if member.Reserved() && member.Status != Paused {
			count++
		}
	}
	for _, effect := range tree.Effects {
		if effect.Kind == "create" && effect.Phase == "create" && effect.State != "confirmed" && effect.State != "failed" && effect.State != "paused" {
			count++
		}
	}
	return count
}

// ReserveSpawn captures the prepared settings and reserves capacity before any
// host creation dispatch. Repeated host calls return the same reservation.
func (s *Service) ReserveSpawn(ctx context.Context, caller Identity, request SpawnRequest) (result Effect, err error) {
	request, err = detach(request)
	if err != nil {
		return
	}
	err = s.update(ctx, caller.Thread, func(tree *Tree, _ *Member) error {
		name, settings, limits := request.Name, request.Settings, request.Limits
		member, e := admitted(tree, caller)
		if e != nil {
			return e
		}
		if caller.Call == "" || name == "" || strings.TrimSpace(request.Message) == "" || !filepath.IsAbs(settings.Workspace) {
			return errors.New("spawn requires host call identity, task name and selected absolute workspace")
		}
		for _, r := range name {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
				return errors.New("task name must contain only lowercase letters, digits, underscores and hyphens")
			}
		}
		task := member.Task + "/" + name
		if old := tree.Effect(caller.Thread, caller.Call); old != nil {
			if old.Kind != "create" || old.Child.Task != task || old.CallerTurn != caller.Turn || old.Message != request.Message || !reflect.DeepEqual(old.Child.Arguments, request.Arguments) {
				return errors.New("conflicting repeated spawn call")
			}
			result = *old
			return unchanged
		}
		for _, old := range tree.Members {
			if old.Task == task {
				return errors.New("task name is already admitted")
			}
		}
		for _, old := range tree.Effects {
			if old.Kind == "create" && old.Child.Task == task && old.State != "failed" {
				return errors.New("task name has a pending or uncertain spawn")
			}
		}
		if member.Limits.Concurrency <= 0 || activeCount(tree) >= member.Limits.Concurrency {
			return errors.New("captured host concurrency limit reached")
		}
		result = Effect{ID: caller.Call, Caller: caller.Thread, CallerTurn: caller.Turn, CallerGeneration: member.Generation, Kind: "create", Phase: "create", State: "pending", Message: request.Message, Generation: 1, Child: &Child{Task: task, Settings: settings, Limits: limits, Arguments: request.Arguments, Import: request.Import}}
		tree.Effects = append(tree.Effects, result)
		return nil
	})
	if err == nil {
		result, err = detach(result)
	}
	return
}
