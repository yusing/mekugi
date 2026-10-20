package executionobservation

import (
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/modules/notifications/notice"
)

func testCriticalErrors() *requestexecution.Failures {
	return requestexecution.NewFailures(new(notice.Service))
}
