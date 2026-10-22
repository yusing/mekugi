package terminalui

import (
	"context"
	"io"

	"github.com/yusing/mekugi/libraries/diffrender"
)

// Pane receives prepared values; painting and input have no source capabilities.
type Pane interface {
	Paint(context.Context, io.Writer, int, int) error
	Title() (string, string)
	Hints() Hints
	Key(string) bool
	Mouse(byte, int, int)
	ClearHover()
	SetTheme(diffrender.Theme)
	Invalidate()
	Reset()
}

type PaneSelection interface {
	Selection(int, int) SelectionSource
}
type PaneNavigator interface {
	NavigatorWidth(int) int
	NavigatorColumns() int
	SetNavigatorColumns(int)
}
type PaneEditor interface {
	EditorPath() string
	Workspace() string
}

// SelectionSource freezes pane-local source geometry and its complete document.
// Source: modules/changehistory/terminalview/pane_selection.go:8:20@d367500b
type SelectionSource struct {
	X, Y, Width int
	Painted     []diffrender.LineSource
	Document    []string
	Sources     []diffrender.LineSource
	Offset      int
	Fixed       int
	Workspace   string
}

// Definitions declares supported placement families with typed service and scope.
// The application supplies C; this library owns no session or source store.
type Definitions[S, C any] struct {
	Auxiliary []PaneDefinition[S, C]
}

type PaneDefinition[S, C any] struct {
	Name, Label string
	Attach      func(diffrender.Theme) PaneBinding[S, C]
}

type PaneFactory[C any] struct {
	Name, Label string
	Attach      func(diffrender.Theme) PaneAttachment[C]
}

// PaneAttachment separates application publication from renderer interaction.
// Only the application retains Prepare; it runs before delivering the pane.
type PaneAttachment[C any] struct {
	Pane    Pane
	Prepare func(C)
}

// PaneBinding constructs a painter independently of the execution source.
// The application supplies the actual service only when preparing local data.
type PaneBinding[S, C any] struct {
	Pane    Pane
	Prepare func(S, C)
}

// BindPane checks the snapshot/view pairing before erasing its concrete types.
// Snapshot reads admitted memory; neither it nor update performs I/O.
func BindPane[S, C, P any, V Pane](snapshot func(S, C) P, create func(diffrender.Theme) V, update func(V, P)) func(diffrender.Theme) PaneBinding[S, C] {
	return func(theme diffrender.Theme) PaneBinding[S, C] {
		pane := create(theme)
		return PaneBinding[S, C]{Pane: pane, Prepare: func(service S, scope C) { update(pane, snapshot(service, scope)) }}
	}
}

// ViewKinds reports declaration metadata without creating views or reading sources.
func (d Definitions[S, C]) ViewKinds() []string {
	if len(d.Auxiliary) == 0 {
		return nil
	}
	return []string{"terminal-auxiliary"}
}

// RosterDefinition selects a feature-owned roster painter. Its concrete value
// type stays checked by composition without a dependency on feature packages.
type RosterDefinition[V any] struct {
	Name, Label string
	New         func() V
}

func (d RosterDefinition[V]) ViewKinds() []string { return []string{"terminal-roster"} }
