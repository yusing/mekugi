package router

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	segments "github.com/yusing/mekugi/modules/commandsegments/segments"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
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
	retained            changes.Scope
	changeSource        changes.Service
	snapshots           *changecapture.Workspaces
	recovered           *recovery.Service
	publishChanges      func([]changes.Update)
	captureNotice       func(string, string, string, string)
	guard               *sessioncontrol.ShellApprovals
	guards              []binding.ShellApproval
	execTrack           *segments.Observer
	skillsManager       bool
}
