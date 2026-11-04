package codexbinding

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
)

func TestRecoveryGuidanceStaysBackendSpecific(t *testing.T) {
	guide, err := frontendGuide([]command.Spec{{Name: "reader", Description: "Read selected content."}}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	base, _, _ := strings.Cut(workflowPreamble, "<!-- child-reply-summary -->")
	_, summaryRecovery, _ := strings.Cut(base, "<!-- summary-recovery -->")
	if strings.Contains(guide.Workflow, summaryRecovery) || !strings.Contains(guide.Workflow, codexWorkflow) {
		t.Fatal("Codex recovery guidance was not projected")
	}
	if !strings.Contains(guide.Shell, strings.ReplaceAll(base, "<!-- summary-recovery -->", "")) || strings.Contains(guide.Shell, codexWorkflow) {
		t.Fatal("Claude workflow changed")
	}
}
