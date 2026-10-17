package router

import (
	"context"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/legacycall"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"

	"github.com/yusing/mekugi/apps/mekugi/internal/frontends"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/nativetrace"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	previewbinding "github.com/yusing/mekugi/modules/livepreview/codexbinding"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"github.com/yusing/mekugi/modules/usage/accounting"
)

// observationFixture owns only integration-test setup. Each request enters the
// production preparer through its public interface and the same borrowed owners.
type observationFixture struct {
	registry         *frontends.Registry
	inputProjections []binding.InputProjection
	usage            *accounting.Service
	review           *sessionreview.Review
	activity         *sessioncontrol.Audience
	execWindows      *executionobservation.Windows
	nativeTrace      *nativetrace.Reader
	control          *sessioncontrol.Observations
	previewTiming    previewbinding.Timing
	calls            *history.CallCache
	retained         changes.Scope
	changeSource     changes.Service
	snapshots        *changecapture.Workspaces
	recovered        *recovery.Service
	publishChanges   func([]changes.Update)
	noticeSink       func(string, string, string, string)
}

func (p *observationFixture) Prepare(ctx context.Context, request *codexresponses.Request, input requestexecution.Preparation) (requestexecution.Prepared, error) {
	preparer := executionobservation.New(executionobservation.Config{
		Registry:         p.registry,
		InputProjections: p.inputProjections,
		Usage:            p.usage,
		Review:           p.review,
		Audience:         p.activity,
		Windows:          p.execWindows,
		NativeTrace:      p.nativeTrace,
		Control:          p.control,
		PreviewTiming:    p.previewTiming,
		Calls:            p.calls,
		Retained:         p.retained,
		Changes:          p.changeSource,
		Checkpoints:      p.snapshots,
		Recovery:         p.recovered,
		PublishChanges:   p.publishChanges,
		Notice:           p.noticeSink,
	})
	return preparer.Prepare(ctx, request, input)
}
func (p *observationFixture) notice(session, thread, category, message string) {
	if p != nil && p.noticeSink != nil {
		p.noticeSink(session, thread, category, message)
	}
}

const (
	applyPatchToolName        = "apply_patch"
	nativeExecCommandToolName = "exec_command"
	codeModeCarrierCustom     = "custom"
	codeModeCarrierFunction   = "function"
)

func retainedReviewFiles(files []diffmodel.ReviewFile) []legacycall.ReviewFile {
	result := make([]legacycall.ReviewFile, len(files))
	for i, f := range files {
		result[i] = legacycall.ReviewFile(f)
	}
	return result
}
func reviewFiles(files []legacycall.ReviewFile) []diffmodel.ReviewFile {
	result := make([]diffmodel.ReviewFile, len(files))
	for i, f := range files {
		result[i] = diffmodel.ReviewFile(f)
	}
	return result
}
func receiptEvidence(h legacycall.History) changepresentation.ReceiptEvidence {
	result := changepresentation.ReceiptEvidence{Files: reviewFiles(h.ReviewFiles), Source: binding.EditReceiptSource(h.ToolName, h.Source), ChangeID: h.ChangeID}
	if h.ExecOutcome != nil {
		result.Labels = h.ExecOutcome.Labels
		result.DeletedDirectories = h.ExecOutcome.DeletedDirectories
	}
	return result
}
