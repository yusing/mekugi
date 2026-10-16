package projection

// Source: internal/router/live_diff_source_bindings.go:1:24@3e523196, provisional source evaluation.

import (
	"time"

	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
)

type previewInterpreterInput struct {
	identity string
	args     []string
	cwd      string
	stdin    string
	deadline time.Time
}

// previewSourceScope adds content prediction to the shared path bindings.
// Prediction remains provisional and never authorizes execution.
type previewSourceScope struct {
	*execscope.Bindings
	input  previewInterpreterInput
	source []byte
	python bool
}
