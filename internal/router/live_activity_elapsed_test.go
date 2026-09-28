package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestLiveActivityElapsedFormatting(t *testing.T) {
	start := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		age  time.Duration
		want string
	}{
		{-time.Second, "0s"}, {0, "0s"}, {999 * time.Millisecond, "0s"},
		{time.Second, "1s"}, {59 * time.Second, "59s"}, {time.Minute, "1m"},
		{5 * time.Minute, "5m"}, {70 * time.Second, "1m10s"},
		{82500 * time.Millisecond, "1m22s"}, {time.Hour, "1h"},
		{time.Hour + time.Minute, "1h1m"}, {time.Hour + time.Second, "1h0m1s"},
		{3682 * time.Second, "1h1m22s"},
	} {
		t.Run(tc.want+"/"+tc.age.String(), func(t *testing.T) {
			now := start.Add(tc.age)
			for _, status := range []string{"Waiting", "Working"} {
				u := appServerUI{view: &liveActivityView{}, turn: "active", status: status, turnStarted: start}
				if got := ansi.Strip(u.sessionLabel(now)); !strings.HasSuffix(got, " "+tc.want) {
					t.Fatalf("composer = %q, want elapsed %q", got, tc.want)
				}
			}
			v := liveActivityView{}
			agent := activityPaneAgent{Name: "/root/worker", Started: start, Responding: true}
			// Before its first response, the agent's last activity is its start.
			last := tc.want + " ago"
			if tc.age < 2*time.Second {
				last = "just now"
			}
			if _, got := v.current(agent, now); got != tc.want+" · "+last {
				t.Fatalf("running agent timer = %q", got)
			}
			agent.LastResponse = now.Add(-time.Minute)
			if _, got := v.current(agent, now); got != tc.want+" · 1m ago" {
				t.Fatalf("responded agent timer = %q", got)
			}
			agent.Responding, agent.LastResponse = false, now
			if _, got := v.current(agent, now.Add(time.Minute)); got != tc.want+" · 1m ago" {
				t.Fatalf("finished agent timer = %q", got)
			}
		})
	}
	if got := liveActivityLast(start, start.Add(82*time.Second)); got != "1m22s ago" {
		t.Fatalf("last response = %q", got)
	}
	if got := liveActivityLast(start, start.Add(time.Second)); got != "just now" {
		t.Fatalf("recent response = %q", got)
	}
}
