package capture

import (
	"path/filepath"
	"time"

	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
)

// Named captures explicit native-tool targets without manufacturing command
// source. Paths must already be resolved against the native working directory;
// workspace retains the selected capture identity.
func Named(paths []string, workspace string) *Observation {
	deadline := time.Now().Add(execCaptureHold)
	base := Observation{Class: execscope.Declared.String(), Roots: AddRoot(nil, workspace)}
	missing := func() *Observation {
		observation := base
		observation.Reason = "capture deadline"
		for _, path := range paths {
			observation.Omitted = append(observation.Omitted, Omission{Path: path, Reason: observation.Reason})
		}
		return &observation
	}
	done := make(chan *Observation, 1)
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case execCaptureSlots <- struct{}{}:
		go func() {
			defer func() { <-execCaptureSlots }()
			capture := newExecCapture(deadline)
			for _, path := range paths {
				if !filepath.IsAbs(path) {
					capture.omit(path, "native target has no absolute path")
					continue
				}
				capture.add(path)
				capture.through(path, "")
			}
			observation := base
			observation.Files, observation.Omitted = capture.files, capture.omitted
			boundExecObservation(&observation)
			done <- &observation
		}()
	case <-timer.C:
		return missing()
	}
	select {
	case observation := <-done:
		if time.Now().Before(deadline) {
			return observation
		}
	case <-timer.C:
	}
	// Reads completing after forwarding cannot establish a before-state.
	return missing()
}
