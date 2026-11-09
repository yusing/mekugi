package control

import (
	"fmt"
	"testing"
	"time"
)

func TestDeadlineWorkStaysWithRuntime(t *testing.T) {
	service, _ := fixture(t)
	for i := range 10 {
		id := fmt.Sprintf("child-%d", i)
		child(t, service, "root", id, "/root/"+id, id+"-turn")
		child(t, service, id, id+"-leaf", "/root/"+id+"/leaf", id+"-leaf-turn")
		_, err := service.RegisterWait(t.Context(), identity(id, id+"-turn"), WaitAny, new(int64(5)), time.Now())
		must(t, err)
		stop(t, service, identity(id, id+"-turn"))
	}
	now := time.Now().Add(time.Hour)
	due := service.Due("root", now)
	t.Logf("deadline scan selected %d runtime jobs for a 10-delegate family", len(due))
	if len(due) != 0 {
		t.Fatalf("root runtime acquired foreign deadline work: %v", due)
	}
	for i := range 10 {
		id := fmt.Sprintf("child-%d", i)
		if due := service.Due(id, now); len(due) != 1 || due[0] != id {
			t.Fatalf("delegate %s deadline work = %v", id, due)
		}
	}
}
