package executionobservation

import (
	"fmt"

	"strings"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func previewViewFixture(id string, rows int) previewmodel.Preview {
	var diff strings.Builder
	for i := 1; i <= rows; i++ {
		fmt.Fprintf(&diff, "+stream_%04d\n", i)
	}
	return previewmodel.Preview{ID: id, Workspace: "/workspace", Thread: "thread",
		Input: diff.String()}
}
