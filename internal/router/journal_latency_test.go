package router

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestJournalDeliveryWorkspaceIsolation(t *testing.T) {
	for _, mode := range []string{"memory", "shared router", "independent routers"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var replay *mekugiReplayStore
				if mode != "memory" {
					var err error
					replay, err = openMekugiReplayStore(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
				}
				delivery, writer := newJournalStore(), newJournalStore()
				if mode != "independent routers" {
					writer = delivery
				}
				if err := writer.initialize(t.Context(), replay, "other-workspace", "other-thread", "/root", ""); err != nil {
					t.Fatal(err)
				}
				release, err := delivery.lockDelivery(t.Context(), replay, "slow-workspace")
				if err != nil {
					t.Fatal(err)
				}
				released := make(chan struct{})
				go func() {
					time.Sleep(3 * time.Second)
					release()
					close(released)
				}()
				defer func() { <-released }()
				started := time.Now()
				ids, err := writer.apply(t.Context(), replay, "other-workspace", "other-thread", "receipt", []journalMutation{{Op: "add", Title: new("Other work")}})
				elapsed := time.Since(started)
				if err != nil || len(ids) != 1 {
					t.Fatalf("independent mutation: ids=%v error=%v", ids, err)
				}
				if elapsed >= time.Second {
					t.Fatalf("independent mutation waited %s for unrelated delivery", elapsed)
				}
				t.Logf("independent mutation wait: %s; unrelated delivery held for 3s", elapsed)
				items, err := writer.list(t.Context(), replay, "other-workspace", "other-thread")
				if err != nil || len(items) != 1 || items[0].Title != "Other work" {
					t.Fatalf("persisted independent mutation: items=%v error=%v", items, err)
				}
				<-released
				if len(delivery.deliveryGates) != 0 || len(writer.deliveryGates) != 0 {
					t.Fatal("idle delivery gates retained")
				}
			})
		})
	}
}

func TestJournalDeliveryCanceledWaiterReleasesGate(t *testing.T) {
	store := newJournalStore()
	release, err := store.lockDelivery(t.Context(), nil, "workspace")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range 20 {
		if unlock, err := store.lockDelivery(ctx, nil, "workspace"); err == nil {
			unlock()
			t.Fatal("canceled waiter acquired delivery")
		}
	}
	release()
	if len(store.deliveryGates) != 0 {
		t.Fatal("canceled waiters retained delivery gates")
	}
}
