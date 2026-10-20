package presentation

import (
	"testing"
	"time"
)

func TestAgentLifecycleObservedAndRestoredTiming(t *testing.T) {
	start := time.Unix(100, 0)
	var agent AgentLifecycle
	agent.ObserveActive(true, start)
	if agent.Responding || agent.WorkTimer.Known {
		t.Fatal("status created an unobserved turn")
	}
	agent.TurnStarted(start)
	agent.ObserveActive(false, start.Add(5*time.Second))
	agent.ObserveActive(true, start.Add(time.Hour))
	agent.TurnCompleted(false, start.Add(time.Hour+3*time.Second))
	if agent.Responding || agent.Final || agent.Turns != 1 || agent.WorkTimer.At(start.Add(2*time.Hour)) != 8*time.Second {
		t.Fatalf("idle gaps or completion changed work: %+v", agent)
	}
	agent.TurnStarted(start.Add(3 * time.Hour))
	agent.TurnCompleted(true, start.Add(3*time.Hour+2*time.Second))
	if !agent.Final || agent.Turns != 2 || agent.Started != start || agent.WorkTimer.At(start.Add(4*time.Hour)) != 10*time.Second {
		t.Fatalf("later turn lost earlier work: %+v", agent)
	}
	// A host-confirmed active resume can be responding without reviving an old
	// work anchor. Open, reversed and missing historical intervals add no time.
	agent.Responding = true
	agent.RestoreTiming(func(yield func(time.Time, time.Time) bool) {
		for _, span := range [][2]time.Time{{start, start.Add(9 * time.Second)}, {start.Add(time.Minute), {}}, {start.Add(30 * time.Second), start}, {{}, {}}} {
			if !yield(span[0], span[1]) {
				return
			}
		}
	})
	if !agent.Responding || !agent.WorkTimer.Since.IsZero() || agent.Started != start || agent.LastResponse != start.Add(9*time.Second) || agent.WorkTimer.At(start.Add(24*time.Hour)) != 9*time.Second {
		t.Fatalf("restoration invented work or changed response admission: %+v", agent)
	}
}
