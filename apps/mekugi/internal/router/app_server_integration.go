package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	segments "github.com/yusing/mekugi/modules/commandsegments/segments"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

// appServerIntegration supplies borrowed owners at native-client construction.
// It owns no resources or state and is not retained as a runtime service.
type appServerIntegration struct {
	controlObservations *sessioncontrol.Observations
	usage               *accounting.Service
	audience            *sessioncontrol.Audience
	review              *sessionreview.Review
	execWindows         *execWindowRegistry
	nativeTrace         *nativetrace.Reader
	retained            *mekugiReplayStore
	captureNotice       func(string, string, string, string)
	guard               *shellApprovalServer
	execTrack           *segments.Observer
	skillsManager       bool
}
