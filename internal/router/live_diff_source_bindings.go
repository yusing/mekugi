package router

import (
	"time"

	"github.com/yusing/mekugi/internal/execscope"
)

type previewInterpreterInput struct {
	identity string
	args     []string
	cwd      string
	stdin    string
	deadline time.Time
}

// previewSourceScope adds content prediction to the shared path bindings.
// Prediction and preview lifecycle remain router-owned.
type previewSourceScope struct {
	*execscope.Bindings
	input  previewInterpreterInput
	source []byte
	python bool
}
