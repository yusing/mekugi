package sudo

import "github.com/yusing/mekugi/contracts/approval"

// Source: internal/router/app_server_approvals.go:69:73,604:612@[bff7c655] exact grants.

// Service creates independent UI-scoped grant sets.
type Service struct{}

func (Service) NewSession() *Session { return &Session{allowed: make(map[approval.Command]bool)} }

// Session is owned by one serialized UI scope and discarded at detach.
type Session struct{ allowed map[approval.Command]bool }

func (s *Session) Allows(command approval.Command) bool { return s.allowed[command] }
func (s *Session) Allow(command approval.Command)       { s.allowed[command] = true }
