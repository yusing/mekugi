// Package presentation defines detached feature publications and their placements.
package presentation

import "github.com/yusing/mekugi/contracts/inference"

type Placement string

const (
	ComposerCaption Placement = "composer-caption"
	AgentMetric     Placement = "agent-metric"
)

// Thread contains already-admitted facts, never a reader or live owner handle.
type Thread struct {
	ID               string
	OutputThroughput inference.OutputThroughput
}

// Status projects one feature's text without terminal formatting or I/O.
// An empty result omits the item. The application binds its stable identity.
type Status struct {
	ID         string
	Placements []Placement
	Text       func(Thread) string
}

type Item struct {
	ID, Thread, Text string
}
