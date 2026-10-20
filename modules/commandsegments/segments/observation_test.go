package segments

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	"github.com/yusing/mekugi/modules/commandsegments/transport"
)

// Claims and messages enter through the same callbacks registered with transport,
// without a FIFO peer obscuring the ordering being exercised here.
func observationFixture() *Observer {
	return &Observer{outputLimit: 64, changed: make(chan struct{}), reports: make(map[[3]string]*report)}
}

func claimObservation(t *testing.T, h *Observer, key [3]string, script string, sources ...string) transport.Report {
	t.Helper()
	h.Start(key, script)
	r := h.claim(t.Context(), transport.Message{Thread: key[0], Script: script, Segments: sources})
	if r == nil {
		t.Fatal("unique host command was not claimed")
	}
	return r
}

func TestObserverExactIdentityAndIdempotentStart(t *testing.T) {
	h := observationFixture()
	first := [3]string{"one", "turn", "first"}
	second := [3]string{"two", "turn", "second"}
	h.Start(first, "printf 'exact '")
	h.Start(first, "printf 'changed'")
	h.Start(second, "printf 'exact '")
	if len(h.started) != 2 || h.sequence != 2 {
		t.Fatalf("duplicate start changed admission: %+v, sequence %d", h.started, h.sequence)
	}
	r := h.claim(t.Context(), transport.Message{Thread: "two", Script: "printf 'exact '", Segments: []string{"printf 'exact '"}})
	if r == nil || !h.Tracking(second) || h.Tracking(first) {
		t.Fatal("thread-qualified claim selected the wrong host item")
	}
	h.Start(second, "printf 'exact '")
	if len(h.started) != 1 || h.sequence != 2 {
		t.Fatal("start during an active report was admitted again")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if h.claim(ctx, transport.Message{Thread: "one", Script: "printf 'exact'"}) != nil {
		t.Fatal("source whitespace was normalized during correlation")
	}
	if h.claim(ctx, transport.Message{Thread: "other", Script: "printf 'exact '"}) != nil {
		t.Fatal("claim crossed thread identity")
	}
}

func TestObserverDelayedStartAndClaimDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := observationFixture()
		claimed := make(chan transport.Report, 1)
		go func() { claimed <- h.claim(t.Context(), transport.Message{Thread: "thread", Script: "echo late"}) }()
		synctest.Wait()
		time.Sleep(150 * time.Millisecond)
		key := [3]string{"thread", "turn", "item"}
		h.Start(key, "echo late")
		if <-claimed == nil || !h.Tracking(key) {
			t.Fatal("delayed host start failed to wake its waiting claim")
		}
		start := time.Now()
		if h.claim(t.Context(), transport.Message{Script: "missing"}) != nil || time.Since(start) != claimWait {
			t.Fatal("unmatched claim did not stop at the bounded deadline")
		}
	})
}

func TestObserverAmbiguousClaimPreservesCandidates(t *testing.T) {
	h := observationFixture()
	h.Start([3]string{"one", "turn", "a"}, "echo same")
	h.Start([3]string{"two", "turn", "b"}, "echo same")
	if h.claim(t.Context(), transport.Message{Script: "echo same"}) != nil || len(h.started) != 2 {
		t.Fatal("unqualified ambiguous claim consumed a candidate")
	}
	h.Start([3]string{"one", "turn", "c"}, "echo same")
	if h.claim(t.Context(), transport.Message{Thread: "one", Script: "echo same"}) != nil || len(h.started) != 3 {
		t.Fatal("same-thread ambiguous claim consumed a candidate")
	}
}

func TestObserverCancellationAndShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := observationFixture()
		ctx, cancel := context.WithCancel(t.Context())
		claimed := make(chan transport.Report, 1)
		go func() { claimed <- h.claim(ctx, transport.Message{Script: "missing"}) }()
		synctest.Wait()
		cancel()
		if <-claimed != nil {
			t.Fatal("canceled waiting claim acquired a report")
		}
	})
	server, err := transport.Listen(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewObserver(server, Instrumentation{}, 64)
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	h.Close()
	h.Start([3]string{"thread", "turn", "item"}, "echo closed")
	if h.claim(t.Context(), transport.Message{Thread: "thread", Script: "echo closed"}) != nil {
		t.Fatal("closed observer accepted a report")
	}
}

func TestSubscriptionWindowAndCrossWindowAmbiguity(t *testing.T) {
	h := observationFixture()
	old := [3]string{"thread", "turn", "old"}
	h.Start(old, "echo old")
	p := h.Subscribe("thread", "turn", []string{"echo old", "echo new"})
	t.Cleanup(p.Close)
	h.claim(t.Context(), transport.Message{Thread: "thread", Script: "echo old"})
	if p.State().Matches[0].Report != nil {
		t.Fatal("subscription admitted a command started before its window")
	}
	h.Start([3]string{"other", "turn", "x"}, "echo new")
	h.Start([3]string{"thread", "other", "y"}, "echo new")
	if p.State().Matches[1].Report != nil {
		t.Fatal("subscription admitted another thread or turn")
	}
	key := [3]string{"thread", "turn", "new"}
	changed := p.State().Changed
	h.Start(key, "echo new")
	select {
	case <-changed:
	default:
		t.Fatal("host start did not notify the observation window")
	}
	if match := p.State().Matches[1]; match.Key != key || match.Report == nil || !match.Report.HostOnly {
		t.Fatalf("future host start missing: %+v", match)
	}
	other := h.Subscribe("thread", "turn", []string{"echo new"})
	t.Cleanup(other.Close)
	if !p.State().Matches[1].Ambiguous || !other.State().Matches[0].Ambiguous {
		t.Fatal("overlapping windows did not withdraw exclusive ownership")
	}
}

func TestSubscriptionDuplicateSourcesAndStartsAreAmbiguous(t *testing.T) {
	h := observationFixture()
	p := h.Subscribe("thread", "turn", []string{"echo duplicate", "echo duplicate", "echo repeated"})
	t.Cleanup(p.Close)
	if !p.State().Matches[0].Ambiguous {
		t.Fatal("duplicate request operands retained exclusive ownership")
	}
	h.Start([3]string{"thread", "turn", "a"}, "echo repeated")
	h.Start([3]string{"thread", "turn", "b"}, "echo repeated")
	if !p.State().Matches[1].Ambiguous {
		t.Fatal("two matching host starts retained exclusive ownership")
	}
}

func TestSubscriptionRetainsFactsWithoutDrainingOutput(t *testing.T) {
	h := observationFixture()
	p := h.Subscribe("thread", "turn", []string{"touch file"})
	t.Cleanup(p.Close)
	key := [3]string{"thread", "turn", "item"}
	r := claimObservation(t, h, key, "touch file", "touch file")
	path := filepath.Join(t.TempDir(), "file")
	r.Observe(transport.Message{Type: transport.Created, Data: path})
	r.Observe(transport.Message{Type: transport.Output, Data: "kept"})
	view := p.State().Matches[0].Report
	if view == nil || view.Segments[0].Output != "" {
		t.Fatal("metadata subscription exposed the output drain")
	}
	view.Created[0].Data = "mutated"
	view.Segments[0].Source = "mutated"
	read, ok := h.Read(key)
	if !ok || read.Segments[0].Output != "kept" || read.Segments[0].Source != "touch file" || read.Created[0].Data != path {
		t.Fatalf("metadata snapshot consumed or aliased report state: %+v", read)
	}
	read, _ = h.Read(key)
	if read.Segments[0].Output != "" {
		t.Fatal("Activity read delivered output twice")
	}
	h.Forget(key)
	if h.Tracking(key) {
		t.Fatal("retired report remains tracked for Activity")
	}
	if paths := p.CreatedPaths(); len(paths) != 1 || paths[0].Data != path {
		t.Fatalf("retirement discarded subscription facts: %+v", paths)
	}
	h.Completed(key)
	if !p.State().Matches[0].HostCompleted {
		t.Fatal("host completion after retirement was lost")
	}
}

func TestObserverHostCompletionAndReportClosureAreIndependent(t *testing.T) {
	h := observationFixture()
	p := h.Subscribe("thread", "turn", []string{"echo done"})
	t.Cleanup(p.Close)
	key := [3]string{"thread", "turn", "item"}
	r := claimObservation(t, h, key, "echo done", "echo done")
	r.Observe(transport.Message{Type: transport.Done, Code: new(7)})
	r.Observe(transport.Message{Type: transport.Closed})
	if p.State().Matches[0].Report.Complete() {
		t.Fatal("closed protocol without stream completion was complete")
	}
	r.End()
	match := p.State().Matches[0]
	if !match.Report.Complete() || match.HostCompleted || match.Report.Code != 7 {
		t.Fatalf("report completion changed host completion: %+v", match)
	}
	h.Completed(key)
	if !p.State().Matches[0].HostCompleted {
		t.Fatal("host completion missing")
	}
	second := [3]string{"thread", "turn", "second"}
	p2 := h.Subscribe("thread", "turn", []string{"echo second"})
	t.Cleanup(p2.Close)
	r2 := claimObservation(t, h, second, "echo second", "echo second")
	h.Completed(second)
	r2.End()
	if match := p2.State().Matches[0]; !match.HostCompleted || match.Report.Complete() {
		t.Fatal("host completion or EOF substituted for missing protocol closure")
	}
}

func TestObserverOverflowDiscardsPendingOutputButKeepsOutcome(t *testing.T) {
	h := observationFixture()
	h.outputLimit = 4
	key := [3]string{"thread", "turn", "item"}
	r := claimObservation(t, h, key, "a; b", "a", "b")
	r.Observe(transport.Message{Type: transport.Output, Index: 0, Data: "abc"})
	r.Observe(transport.Message{Type: transport.Output, Index: 1, Data: "1234"})
	r.Observe(transport.Message{Type: transport.Output, Index: 0, Data: "de"})
	r.Observe(transport.Message{Type: transport.Output, Index: 1, Data: "ignored"})
	r.Observe(transport.Message{Type: transport.End, Index: 1, Code: new(3)})
	view, _ := h.Read(key)
	if !view.Lossy || view.Segments[0].Output != "" || view.Segments[1].Output != "" || !view.Segments[1].Ended || view.Segments[1].Code != 3 {
		t.Fatalf("overflow did not force aggregate fallback while retaining outcome: %+v", view)
	}
}

func TestObserverTimingAndMalformedFacts(t *testing.T) {
	h := observationFixture()
	key := [3]string{"thread", "turn", "item"}
	r := claimObservation(t, h, key, "a && b", "a", "b")
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	timing := transport.Timing{Started: start, Ended: start.Add(time.Second), ElapsedNS: 123}
	r.Observe(transport.Message{Type: transport.Begin, Timing: transport.Timing{Started: start}})
	r.Observe(transport.Message{Type: transport.End})
	r.Observe(transport.Message{Type: transport.Done})
	r.Observe(transport.Message{Type: transport.Output, Index: -1, Data: "invalid"})
	r.Observe(transport.Message{Type: transport.Output, Index: 2, Data: "invalid"})
	r.Observe(transport.Message{Type: transport.Created, Data: "relative"})
	r.Observe(transport.Message{Type: "unknown", Data: "invalid"})
	view, _ := h.Read(key)
	if view.Done || view.Segments[0].Ended || view.Segments[0].Output != "" || len(view.Created) != 0 || view.LastStarted() != 0 {
		t.Fatalf("malformed facts invented completion or output: %+v", view)
	}
	r.Observe(transport.Message{Type: transport.End, Code: new(0), Timing: timing})
	view, _ = h.Read(key)
	if view.Segments[0].Timing != timing || !view.Segments[0].Ended || view.Segments[1].Began {
		t.Fatalf("valid timing or short-circuited operand changed: %+v", view)
	}
	timing.Started = start.Add(time.Second)
	r.Observe(transport.Message{Type: transport.End, Code: new(0), Timing: timing})
	view, _ = h.Read(key)
	if !view.Segments[0].Timing.Started.Equal(start) || view.Segments[0].Timing.ElapsedNS != 123 {
		t.Fatal("mismatched end timing replaced validated helper timing")
	}
}

func TestSubscriptionGuardIdentity(t *testing.T) {
	for _, command := range []struct {
		name, tail string
		vcs        bool
	}{
		{"vcs", "git --version", true},
		{"sudo", "sudo id", false},
		{"mixed", "git --version; sudo id", true},
		{"vcs disabled", "git --version; sudo id", false},
	} {
		t.Run(command.name, func(t *testing.T) {
			original := "gofmt -w source.go; " + command.tail
			const guard = "/private/session/vcs-guard"
			for _, scenario := range []string{"current", "item-bound", "wrong item", "captured guarded", "foreign", "pending duplicate", "lookalike helper"} {
				t.Run(scenario, func(t *testing.T) {
					h := observationFixture()
					h.instrumentation = Instrumentation{VCS: guard, Sudo: "/private/session/sudo-guard"}
					script, directory := original, guard
					sudoDirectory := "/private/session/sudo-guard"
					if !command.vcs {
						directory = ""
					}
					if scenario == "foreign" {
						if command.vcs {
							directory = "/private/other/vcs-guard"
						}
						sudoDirectory = "/private/other/sudo-guard"
					}
					if scenario == "lookalike helper" {
						if command.vcs {
							script = strings.Replace(script, "git --version", "/usr/bin/git --version", 1)
						} else {
							script = strings.Replace(script, "sudo id", "/usr/bin/sudo id", 1)
						}
					}
					item := ""
					if scenario == "item-bound" {
						item = "command"
					} else if scenario == "wrong item" {
						item = "other"
					}
					wrapped, err := shellinstrumentation.RewriteCommands(script, "/unrelated/mekugi-exec", directory, sudoDirectory, item)
					if err != nil {
						t.Fatal(err)
					}
					captured := script
					if scenario == "captured guarded" {
						captured = wrapped
					}
					p := h.Subscribe("main", "turn", []string{captured})
					t.Cleanup(p.Close)
					key := [3]string{"main", "turn", "command"}
					claimObservation(t, h, key, wrapped)
					h.Completed(key)
					if scenario == "pending duplicate" {
						h.Start([3]string{"main", "turn", "pending"}, original)
					}
					match := p.State().Matches[0]
					settled := !match.Ambiguous && match.Report != nil && match.HostCompleted
					if settled != (scenario == "current" || scenario == "item-bound" || scenario == "captured guarded") {
						t.Fatalf("usable matching evidence = %v: %+v", settled, match)
					}
				})
			}
		})
	}
}

func TestObserverReleaseOutputRetainsOutcome(t *testing.T) {
	h := observationFixture()
	key := [3]string{"thread", "turn", "item"}
	r := claimObservation(t, h, key, "echo done", "echo done")
	r.Observe(transport.Message{Type: transport.Output, Data: "pending"})
	r.Observe(transport.Message{Type: transport.End, Code: new(7)})
	r.Observe(transport.Message{Type: transport.Done, Code: new(7)})
	r.Observe(transport.Message{Type: transport.Closed})
	r.End()
	h.ReleaseOutput(key)
	r.Observe(transport.Message{Type: transport.Output, Data: "future"})
	view, ok := h.Read(key)
	if !ok || !view.Complete() || !view.Segments[0].Ended || view.Segments[0].Code != 7 || view.Code != 7 {
		t.Fatalf("output release discarded completed outcome: %+v", view)
	}
	if !view.Lossy || view.Segments[0].Output != "" {
		t.Fatalf("output after attachment loss became usable retained output: %+v", view)
	}
}
