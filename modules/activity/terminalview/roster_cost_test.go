package terminalview

import (
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/diffrender"
	"github.com/yusing/mekugi/modules/activity/presentation"
)

func rosterCostAgent() presentation.Agent {
	return presentation.Agent{
		AgentFacts: presentation.AgentFacts{Name: "/root/worker", Responding: true,
			Started: time.Unix(100, 0), WorkKnown: true, WorkSince: time.Unix(100, 0)},
		Summary: []presentation.Entry{{Seq: 1, Values: []presentation.Value{{
			Kind: "op", Verb: "Run", Fenced: true, Lang: "javascript",
			Code: "const results = await Promise.allSettled([tools.exec_command({cmd: 'go test ./...'})]);\n" + strings.Repeat("// retained source\n", 1000),
		}}}},
	}
}

func TestRosterSummaryRetainsSyntaxCache(t *testing.T) {
	v := NewView()
	agent, now := rosterCostAgent(), time.Unix(110, 0)
	v.Painter.Clock = func() time.Time { return time.Unix(1, 0) }
	want, _ := v.Current(agent, now, 80)
	allocs := testing.AllocsPerRun(20, func() {
		if got, _ := v.Current(agent, now, 80); got != want {
			t.Fatal("unchanged summary changed")
		}
	})
	if allocs > 100 {
		t.Fatalf("unchanged summary repeats syntax work: %.0f allocations", allocs)
	}
	if !v.Painter.now().Equal(time.Unix(1, 0)) {
		t.Fatal("summary replaced the painter's clock")
	}
	v.Painter.Theme = diffrender.LightTheme
	fresh := NewView()
	fresh.Painter.Theme = diffrender.LightTheme
	want, _ = fresh.Current(agent, now, 20)
	if got, _ := v.Current(agent, now, 20); got != want {
		t.Fatal("cached source ignored theme or width")
	}
	parts := RosterMetricParts(v, agent, now.Add(time.Second))
	if parts[2] != "11s" {
		t.Fatalf("timer stopped advancing: %q", parts[2])
	}
	v.Clock = func() time.Time { return now }
	frozen := v.FreezeTranscript()
	agent.Summary = []presentation.Entry{{Seq: 2, Live: true, Values: []presentation.Value{{Kind: "progress", Body: "Working", Live: true}}}}
	first, _ := frozen.Current(agent, now)
	second, _ := frozen.Current(agent, now.Add(time.Second))
	if first == second {
		t.Fatal("live summary pulse ignored frame time")
	}
	if !frozen.Painter.now().Equal(now) || !v.Painter.now().Equal(time.Unix(1, 0)) {
		t.Fatal("summary changed a frozen or independent painter clock")
	}
}

func BenchmarkNativeRosterRetainedSource(b *testing.B) {
	v := NewView()
	agent, now := rosterCostAgent(), time.Unix(110, 0)
	v.Update(&presentation.Publication{Roster: []presentation.AgentRow{{Agent: agent}}}, Display{Caller: "/root"})
	v.NativeRoster(120, 4, now, false)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		v.NativeRoster(120, 4, now, false)
	}
}
