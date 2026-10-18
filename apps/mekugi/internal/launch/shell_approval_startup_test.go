package launch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/shellapproval"

	segmentservice "github.com/yusing/mekugi/modules/commandsegments/segments"

	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

func TestShellApprovalStartupSurvivesReportFailure(t *testing.T) {
	for _, guard := range []bool{true, false} {
		for _, phase := range []string{"acquire", "attach"} {
			t.Run(fmt.Sprintf("guard=%v/%s", guard, phase), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				segments := &segmentservice.Service{Reports: testReports(), LiveError: errors.New("injected report acquisition failure")}
				if phase == "attach" {
					var err error
					segments.Live, err = segmenttransport.Listen(ctx)
					if err != nil {
						t.Fatal(err)
					}
					segments.Live.Close()
					segments.LiveError = nil
				}
				registry, err := buildToolRegistryForTest(t, ctx)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := registry.Close(); err != nil {
						t.Error(err)
					}
				})
				guards := testGuards()
				if !guard {
					guards = nil
				}
				approvals, reports, err := openShellObservation(ctx, registry.FrontendDirectory, guards, segments, testCriticalErrors())
				if err != nil {
					t.Fatalf("report failure blocked startup: %v", err)
				}
				defer approvals.Close()
				if reports != nil {
					reports.Close()
					t.Fatal("failed report transport acquired an observer")
				}
				_, channel := shellapproval.Paths(registry.FrontendDirectory)
				socket, linkErr := os.Readlink(channel)
				if !guard {
					if !errors.Is(linkErr, os.ErrNotExist) {
						t.Errorf("disabled shell guard acquired approval channel: %v", linkErr)
					}
					return
				}
				if linkErr != nil {
					t.Fatalf("report failure removed approval channel: %v", linkErr)
				}
				conn, dialErr := net.DialTimeout("unix", socket, time.Second)
				if dialErr != nil {
					t.Fatalf("report failure disabled approval requests: %v", dialErr)
				}
				conn.Close()
			})
		}
	}
}
