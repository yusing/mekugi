package router

import "testing"

func TestMainResponseUsageIsolationAtRequestBoundary(t *testing.T) {
	p := newManagedMekugiProxy(t)
	root, _ := prepareActivityTest(t, p, "session", "thread", "", "/root", nil)
	if !root.activityResponding || p.activity.threads["thread"].responding != 1 {
		t.Fatal("accepted main request did not start response tracking")
	}
	p.activity.observe("child", "thread", "/root/child", true)
	p.activity.observe("other", "", "/root", false)
	counts := tokenCounts{InputTokens: 100, UncachedInputTokens: 100, OutputTokens: 20}
	root.observeResponseUsage(counts)
	root.Close()
	node := p.activity.threads["thread"]
	if node.responding != 0 || node.turns != 1 || node.lastResponse.IsZero() {
		t.Fatalf("main request lifecycle not reflected in response tracking: %+v", node)
	}
	if report, ok := p.activity.usage.snapshot("thread"); !ok || report.InputTokens != 100 || report.OutputTokens != 20 {
		t.Fatalf("main usage not reflected in the canonical usage owner: %+v", report)
	}
	for _, thread := range []string{"child", "other"} {
		if _, observed := p.activity.usage.snapshot(thread); observed || p.activity.threads[thread].turns != 0 {
			t.Fatalf("main usage or lifecycle leaked into %s", thread)
		}
	}
	root.Close()
	if node.responding != 0 || node.turns != 1 {
		t.Fatal("repeated close altered main lifecycle")
	}
}
