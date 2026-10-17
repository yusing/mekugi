package router

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"time"

	history "github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/modules/diagnostics/diagnostic"
)

func (c *CriticalErrors) persistFailure(f *requestFinalization) {
	if c == nil || f.diagnosticReference == "" {
		return
	}
	c.mu.Lock()
	store := c.failureStore
	limits := c.failureLimits
	writes := c.writes
	service := c.diagnostics
	c.mu.Unlock()
	if service == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if store == nil {
		directory, err := history.ReplayDirectory()
		if err == nil {
			store, err = service.OpenFailures(ctx, directory)
		}
		if err == nil {
			c.mu.Lock()
			c.failureStore = store
			c.mu.Unlock()
		}
	}
	var err error
	if store == nil {
		err = errors.New("failure storage unavailable")
	} else {
		record := diagnostic.Failure{Version: 1, Time: time.Now().UTC(), Thread: f.threadID,
			Phase: string(f.failurePhase), Code: f.diagnosticCode, Reference: f.diagnosticReference,
			Error: f.diagnosticError}
		if f.streamDiagnostics != nil {
			record.Stream, err = json.Marshal(f.streamDiagnostics.Snapshot())
		}
		if err == nil {
			err = service.RetainFailure(ctx, store, writes, limits, record)
		}
	}
	if err != nil {
		c.addThreadNotice(f.sessionID, f.threadID, "failure_storage", "Mekugi could not retain the failure reference. Relaunch with --debug to capture a future failure.")
	}
}
