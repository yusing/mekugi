// Package control owns logical thread coordination independently of host turns.
package control

import (
	"encoding/json/jsontext"
	"github.com/yusing/mekugi/contracts/guidance"
	"time"
)

type Status string

const (
	Pending     Status = "pending"
	Working     Status = "working"
	Waiting     Status = "waiting"
	Paused      Status = "paused"
	Completed   Status = "completed"
	Failed      Status = "failed"
	Interrupted Status = "interrupted"
	Uncertain   Status = "uncertain"
)

// Identity is supplied by the authenticated Codex binding, never tool arguments.
type Identity struct{ Thread, Session, Turn, Call, Item string }

// Participant is detached roster identity, without instructions or dispatch state.
type Participant struct {
	Thread, Parent, Task, Role string
	Status                     Status
	NonAttachable              bool
}

type Settings struct {
	Workspace, Model, Provider, Role, PermissionID, ApprovalsReviewer string
	Effort, Summary, Tier, BaseInstructions, DeveloperInstructions    *string
	RuntimeRoots                                                      []string
	PermissionProfile, ApprovalPolicy, Config                         jsontext.Value
	Preparation                                                       jsontext.Value
	NonAttachable                                                     bool
	SelectedRole                                                      *RoleSelection
}

type RoleSelection struct{ Name, Description, Path, Digest, Contents string }
type Limits struct {
	MinWaitMS, MaxWaitMS int64
}

type Member struct {
	Path, Thread, Session, Parent, Task, Turn, LastAnswer string
	answerItems                                           []string
	Generation                                            uint64
	Status                                                Status
	AudienceAdmitted, HostRunning                         bool
	Settings                                              Settings
	Limits                                                Limits
	Instructions                                          []guidance.Fragment
	Wait                                                  *Wait
	RecoveryWait                                          *Wait
	Stop                                                  *LocalStop
	Preload                                               ContextPreload `json:",omitzero"`
}

// ContextPreload binds a frozen source selection to its retained assignment.
type ContextPreload struct{ Item, Selection string }

// A paused generation retains its admitted work identity.
func (m *Member) Reserved() bool {
	return m != nil && (m.Status == Pending || m.Status == Working || m.Status == Waiting || m.Status == Paused || m.Status == Uncertain)
}

type LocalStop struct {
	Generation       uint64
	Turn, Item, Wait string
}

type HostFailure struct {
	Message     string
	Recoverable bool
}

// Wait owns its continuation intent, including dispatch and uncertainty.
type Wait struct {
	Effect
	Metrics                  map[string]SessionMetrics
	Item, Stop, ResumeHandle string
	Targets                  []string
	All                      bool
	Recovery                 map[string]uint64
	RequestedMS, EffectiveMS *int64
	Deadline                 *time.Time
	Terminal                 *Event
}

// View exposes wait state without checkpoint settings or effect payloads.
func (wait *Wait) View(tree *Tree) map[string]any {
	if wait == nil {
		return nil
	}
	result := map[string]any{"id": wait.ID, "caller": wait.Caller, "generation": wait.Generation, "turn": wait.Turn, "call": wait.ID, "item": wait.Item, "targets": wait.Targets, "requested_ms": wait.RequestedMS, "effective_ms": wait.EffectiveMS, "deadline": wait.Deadline, "state": wait.PublicState(), "stop": wait.Stop, "resume_handle": wait.ResumeHandle}
	if event := tree.waitResult(wait).Event; event != nil {
		result["event"] = event.ID
	}
	return result
}

type Event struct {
	ID         string `json:"id"`
	Recipient  string `json:"recipient"`
	Source     string `json:"source"`
	Task       string `json:"task"`
	Turn       string `json:"turn"`
	Generation uint64 `json:"generation"`
	Kind       string `json:"kind"`
	Status     Status `json:"status"`
	Message    string `json:"message,omitempty"`
}

type Tree struct {
	Version int
	Root    string
	Members map[string]*Member
	Effects []Effect
}

// Effect retains attributed control intent before a host effect is dispatched.
// Creation advances through fixed phases; Turn is frozen at dispatch claim.
type Effect struct {
	ID, Caller, CallerTurn, Target, Kind, Message, State, Phase, Turn, Error, ClaimedBy string
	CallerGeneration, Generation                                                        uint64
	Consumed                                                                            bool
	PreviousStatus, Outcome                                                             Status
	Child                                                                               *Child
	Wake                                                                                *WaitResult `json:"-"`
}

// PublicState describes pending waits without exposing their checkpoint phase.
func (effect Effect) PublicState() string {
	if effect.Kind == "wake" && effect.State == "registered" {
		return "waiting"
	}
	return effect.State
}

// SpawnRequest freezes one host call's preparation and assignment together.
type SpawnRequest struct {
	Import, Name, Message string
	Preload               string
	Arguments             jsontext.Value
	Settings              Settings
	Limits                Limits
}

type Child struct {
	Import, Task, Session, Path string
	Arguments                   jsontext.Value
	Settings                    Settings
	Limits                      Limits
	Preload                     ContextPreload `json:",omitzero"`
}

// Receipt contains only facts returned by the sole host request owner.
type Receipt struct {
	Thread, Session, Path, Turn, Error, Phase string
	Uncertain                                 bool
	Reconciled                                bool
}

type WaitResult struct {
	Instructions string                    `json:"instructions,omitempty"`
	Metrics      map[string]SessionMetrics `json:"metrics,omitempty"`
	ID           string                    `json:"-"`
	All          bool                      `json:"-"`
	State        string                    `json:"state"`
	Event        *Event                    `json:"event,omitempty"`
	Targets      []string                  `json:"targets,omitempty"`
	EffectiveMS  *int64                    `json:"effective_ms,omitempty"`
}

func (tree *Tree) Effect(caller, id string) *Effect {
	for i := range tree.Effects {
		if tree.Effects[i].Caller == caller && tree.Effects[i].ID == id {
			return &tree.Effects[i]
		}
	}
	if member := tree.Members[caller]; member != nil && member.Wait != nil && member.Wait.ID == id {
		return &member.Wait.Effect
	}
	return nil
}

// Actions includes embedded continuations without a second durable wake record.
func (tree *Tree) Actions() []Effect {
	actions := append([]Effect(nil), tree.Effects...)
	for _, member := range tree.Members {
		if member.Wait != nil {
			action := member.Wait.Effect
			action.Wake = tree.wakeResult(member.Wait)
			actions = append(actions, action)
		}
	}
	return actions
}

func (tree *Tree) edit(thread string) *Member {
	if current := tree.Members[thread]; current != nil {
		member := *current
		if current.Wait != nil {
			member.Wait = new(*current.Wait)
		}
		if current.RecoveryWait != nil {
			member.RecoveryWait = new(*current.RecoveryWait)
		}
		tree.Members[thread] = &member
		return &member
	}
	return nil
}
