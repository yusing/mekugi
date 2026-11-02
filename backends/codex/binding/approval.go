package binding

import (
	"errors"
	"slices"
)

type ShellGuardKind string

const (
	RemoteWrite ShellGuardKind = ""
	Sudo        ShellGuardKind = "sudo"
)

// GuardForRequest maps private wire kinds to the registered policy owner.
func GuardForRequest(kind string) (ShellGuardKind, bool) {
	switch kind {
	case "":
		return RemoteWrite, true
	case "sudo", "sudo-password":
		return Sudo, true
	default:
		return "", false
	}
}

type ShellApproval struct {
	Kind ShellGuardKind
}

func (r *Registry) ShellApproval(guard ShellApproval) error {
	if guard.Kind != RemoteWrite && guard.Kind != Sudo {
		return errors.New("invalid shell approval registration")
	}
	if slices.ContainsFunc(r.guards, func(old ShellApproval) bool { return old.Kind == guard.Kind }) {
		return errors.New("duplicate shell approval registration")
	}
	r.guards = append(r.guards, guard)
	return nil
}
func (r *Registry) RegisteredGuards() []ShellApproval { return slices.Clone(r.guards) }
