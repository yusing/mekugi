package control

import (
	"fmt"
	"testing"
	"time"
)

func TestCompactionCheckpointAdmission(t *testing.T) {
	for _, evidence := range []string{"yielded", "failed", "stale", "success"} {
		t.Run(evidence, func(t *testing.T) {
			s, _ := fixture(t)
			id := identity("root", "rt")
			result, err := s.RegisterCompaction(t.Context(), id, nil, false, "", nil, time.Now(), true)
			must(t, err)
			if result.State != "scheduled" {
				t.Fatal(result)
			}
			turn, handle, success := id.Turn, "", evidence == "success"
			if evidence == "stale" {
				turn = "old"
				success = true
			}
			if evidence == "yielded" {
				handle = "cell:running"
			}
			must(t, s.Execution(t.Context(), id.Thread, turn, id.Item, handle, success))
			local, err := s.ClaimLocalStop(t.Context(), id.Thread, id.Turn, id.Item)
			must(t, err)
			if (local != nil) != (evidence == "success") {
				t.Fatalf("stop=%+v", local)
			}
			member, _ := s.Member(id.Thread)
			if evidence == "failed" && member.Wait.State != "canceled" {
				t.Fatal(member.Wait)
			}
			if evidence != "success" {
				return
			}
			effect, err := s.ClaimEffect(t.Context(), id.Thread, id.Call+":compact")
			must(t, err)
			if effect != nil {
				t.Fatal("compaction dispatched before host settlement")
			}
			must(t, s.HostCompleted(t.Context(), id.Thread, id.Turn, Completed, HostFailure{}))
			effect, err = s.ClaimEffect(t.Context(), id.Thread, id.Call+":compact")
			must(t, err)
			if effect == nil {
				t.Fatal("settled checkpoint lost compaction")
			}
			must(t, s.EffectAcknowledged(t.Context(), id.Thread, effect.ID, Receipt{}))
			wake, err := s.PrepareWake(t.Context(), id.Thread, time.Now())
			must(t, err)
			if wake == nil {
				t.Fatal("standalone compaction did not continue")
			}
			second, err := s.PrepareWake(t.Context(), id.Thread, time.Now())
			must(t, err)
			if second != nil {
				t.Fatal("duplicate continuation")
			}
			claimed, err := s.ClaimEffect(t.Context(), id.Thread, wake.ID)
			must(t, err)
			must(t, s.EffectAcknowledged(t.Context(), id.Thread, claimed.ID, Receipt{Turn: "continued"}))
			if publication := s.WaitPublication(id.Thread); publication.State != "completed" {
				t.Fatalf("successful checkpoint published as %+v", publication)
			}
		})
	}
}

func TestCompoundCompactionRetainsWaitAndEvents(t *testing.T) {
	for _, mode := range []WaitMode{WaitAny, WaitAll} {
		for _, arrival := range []string{"before", "during", "after", "timeout", "pending"} {
			for _, failed := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/failed=%t", mode, arrival, failed), func(t *testing.T) {
					s, _ := fixture(t)
					ctx := t.Context()
					now := time.Unix(100, 0)
					child(t, s, "root", "child", "/root/child", "ct")
					id := identity("root", "rt")
					sender := identity("child", "ct")
					if arrival == "before" {
						message(t, s, sender, "review ready")
					}
					var timeout *int64
					if arrival == "timeout" {
						timeout = new(int64(10))
					}
					result, err := s.RegisterCompaction(ctx, id, nil, true, mode, timeout, now, false)
					must(t, err)
					if result.State != "scheduled" || result.Event != nil {
						t.Fatalf("ready outcome bypassed compact: %+v", result)
					}
					stop(t, s, id)
					compact, err := s.ClaimEffect(ctx, id.Thread, id.Call+":compact")
					must(t, err)
					if compact == nil {
						t.Fatal("no compact dispatch")
					}
					var queued *Effect
					if arrival == "during" {
						effect, err := s.PrepareAction(ctx, sender, "/root", "message", "review ready")
						must(t, err)
						queued = &effect
						claimed, err := s.ClaimEffect(ctx, sender.Thread, effect.ID)
						must(t, err)
						if claimed != nil {
							t.Fatal("message crossed compact barrier")
						}
					}
					must(t, s.InputConsumed(ctx, id, []string{MessageItemID(sender.Thread + ":" + sender.Call)}))
					wake, err := s.PrepareWake(ctx, id.Thread, now.Add(time.Second))
					must(t, err)
					if wake != nil {
						t.Fatal("wake crossed compact barrier")
					}
					receipt := Receipt{}
					if failed {
						receipt.Error = "compact failed"
					}
					must(t, s.EffectAcknowledged(ctx, id.Thread, compact.ID, receipt))
					if arrival == "after" {
						effect, err := s.PrepareAction(ctx, sender, "/root", "message", "review ready")
						must(t, err)
						queued = &effect
					}
					if queued != nil {
						claimed, err := s.ClaimEffect(ctx, sender.Thread, queued.ID)
						must(t, err)
						if claimed == nil {
							t.Fatal("compaction settlement lost retained input")
						}
						must(t, s.EffectAcknowledged(ctx, sender.Thread, queued.ID, Receipt{}))
					}
					wake, err = s.PrepareWake(ctx, id.Thread, now.Add(time.Second))
					must(t, err)
					if arrival == "pending" {
						if wake != nil {
							t.Fatal("pending wait started inference")
						}
						return
					}
					if wake == nil {
						t.Fatal("settled compaction lost wait outcome")
					}
					claimed, err := s.ClaimEffect(ctx, id.Thread, wake.ID)
					must(t, err)
					if claimed == nil || claimed.Wake == nil || claimed.Wake.Compaction == nil {
						t.Fatal("missing compaction outcome")
					}
					if failed && claimed.Wake.Compaction.Error != "compact failed" {
						t.Fatal(claimed.Wake)
					}
					if arrival == "timeout" && claimed.Wake.State != "timeout" {
						t.Fatal(claimed.Wake)
					}
					member, _ := s.Member(id.Thread)
					if member.Status != Waiting || member.Generation != 1 {
						t.Fatal("auxiliary work finished assignment")
					}
				})
			}
		}
	}
}

func TestCompactionSupersessionAndUncertainty(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprint(uncertain), func(t *testing.T) {
			s, _ := fixture(t)
			ctx := t.Context()
			id := identity("root", "rt")
			_, err := s.RegisterCompaction(ctx, id, nil, false, "", nil, time.Now(), true)
			must(t, err)
			stop(t, s, id)
			if uncertain {
				effect, err := s.ClaimEffect(ctx, id.Thread, id.Call+":compact")
				must(t, err)
				must(t, s.EffectAcknowledged(ctx, id.Thread, effect.ID, Receipt{Uncertain: true, Error: "lost acknowledgement"}))
				effect, err = s.ClaimEffect(ctx, id.Thread, effect.ID)
				must(t, err)
				if effect != nil {
					t.Fatal("uncertain compaction was retried")
				}
				next := identity("root", "next-turn")
				must(t, s.RequestStarted(ctx, next, true))
				if _, err := s.RegisterCompaction(ctx, next, nil, false, "", nil, time.Now(), true); err == nil {
					t.Fatal("new call replaced uncertain compaction")
				}
			} else {
				_, err = s.HumanInterrupt(ctx, id.Thread, id.Call)
				must(t, err)
			}
			wake, err := s.PrepareWake(ctx, id.Thread, time.Now())
			must(t, err)
			if wake != nil {
				t.Fatal("superseded or uncertain compaction continued")
			}
		})
	}
}
