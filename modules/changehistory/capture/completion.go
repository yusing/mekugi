package capture

// CommandOutcome is admitted host evidence for one nested command, not a
// conclusion inferred from source or the outer cell's result.
type CommandOutcome struct {
	Completed bool
	Exit      *int
}

type Completion struct {
	CodeMode, Succeeded bool
	Exit                *int
	Confirmed           bool
	Commands            []CommandOutcome
}

// ApplyCompletions reduces a nonempty sibling batch while retaining its capture
// facts. Failure dominates unknown, and unknown dominates success. The returned
// member flags select failed host reports without changing their original text.
// Source: apps/mekugi/internal/router/exec_observation.go:201:242@1bcbdd5a.
func (o *Outcome) ApplyCompletions(members []Completion) []bool {
	failed := make([]bool, len(members))
	o.Status, o.Exit = "", nil
	for index, member := range members {
		exit := member.Exit
		status := StatusCompleted
		switch {
		case member.CodeMode && member.Succeeded:
			status = ""
		case !member.Succeeded:
			status = StatusFailed
		}
		if member.Confirmed {
			status = StatusCompleted
			for _, command := range member.Commands {
				if !command.Completed || command.Exit == nil || *command.Exit != 0 {
					status = StatusFailed
				}
			}
			if len(member.Commands) == 1 {
				exit = member.Commands[0].Exit
			}
		}
		if len(members) == 1 && exit != nil {
			o.Exit = new(*exit)
		}
		if index == 0 || status == StatusFailed || status == "" && o.Status != StatusFailed {
			o.Status = status
		}
		failed[index] = status == StatusFailed
	}
	return failed
}
