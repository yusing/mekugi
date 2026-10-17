package router

import (
	"testing"

	"github.com/yusing/mekugi/contracts/inference"
)

func TestMainResponseUsageIsolationAtRequestBoundary(t *testing.T) {
	p := newManagedMekugiProxy(t)
	root, _ := prepareActivityTest(t, p, "session", "thread", "", "/root", nil)
	p.activity.observe("child", "thread", "/root/child", true)
	p.activity.observe("other", "", "/root", false)
	counts := inference.Usage{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 20}
	root.ObserveUsage(counts)
	root.Close()
	if report, ok := p.usage.Snapshot("thread"); !ok || report.InputTokens != 100 || report.OutputTokens != 20 {
		t.Fatalf("main usage not reflected in the canonical usage owner: %+v", report)
	}
	for _, thread := range []string{"child", "other"} {
		if _, observed := p.usage.Snapshot(thread); observed {
			t.Fatalf("main usage leaked into %s", thread)
		}
	}
}
