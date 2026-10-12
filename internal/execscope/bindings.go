package execscope

import (
	"time"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// BindingState holds syntax-derived candidates. Preview consumers rebind them in
// statement order; they never represent values obtained by running a program.
type BindingState struct {
	Paths    map[string][]string
	Segments map[string][]string
	Aliases  map[string]string
}

func newBindingState() BindingState {
	return BindingState{Paths: make(map[string][]string), Aliases: make(map[string]string)}
}

// SourceInput selects syntax and bounds. IntentOnly forbids file/provider reads.
type SourceInput struct {
	Source       []byte
	Script       string
	Directory    string
	Deadline     time.Time
	Python       bool
	IntentOnly   bool
	TemporaryEnv map[string]string
}

// Bindings shares source path derivation with ordered literal previews.
// Each instance belongs to one parse tree and one consumer.
type Bindings struct{ scan execSourceScope }

func NewBindings(input SourceInput) *Bindings {
	return &Bindings{scan: execSourceScope{input: execProviderInput{cwd: input.Directory, deadline: input.Deadline}, source: input.Source, script: input.Script, python: input.Python, intentOnly: input.IntentOnly, temporaryEnv: input.TemporaryEnv, state: newBindingState(), assigned: make(map[string]int), texts: make(map[string]bool)}}
}
func (b *Bindings) State() *BindingState                     { return &b.scan.state }
func (b *Bindings) Text(node *sitter.Node) string            { return b.scan.text(node) }
func (b *Bindings) Literal(node *sitter.Node) (string, bool) { return b.scan.literal(node) }
func (b *Bindings) Paths(node *sitter.Node) []string         { return b.scan.paths(node) }
func (b *Bindings) Segments(node *sitter.Node) []string {
	return b.scan.literalSegments(node, 0)
}

type SourceResult struct {
	Scope      []Entry
	Writes     bool
	Unresolved bool
}

func (b *Bindings) Analyze(node *sitter.Node) SourceResult {
	b.scan.walk(node)
	if !b.scan.python {
		// A later loop can rebind a multiply assigned name. Keep built write
		// scopes, but do not lend that ambiguous final binding to previews.
		for name, count := range b.scan.assigned {
			if count > 1 {
				delete(b.scan.state.Paths, name)
			}
		}
	}
	return SourceResult{Scope: b.scan.result.scope, Writes: b.scan.writes, Unresolved: b.scan.result.open}
}
