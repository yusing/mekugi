package router

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
				options, err := ParseSessionConfig([]string{"--mode", "mekugi", "--ansi-faint", "off", fmt.Sprintf("--shell-guard=%v", guard)})
				if err != nil {
					t.Fatal(err)
				}
				ready := false
				err = RunSession(testHistory(), testRecovery(), segments, testChanges(), ctx, testCommands(), testInputProjections(), testGuidance(), testNaming(), testGuards(), options, NewCriticalErrors(), func(session Session) {
					ready = true
					defer cancel()
					// Stop at backend launch, after the production approval startup boundary.
					backend := exec.Command(filepath.Join(t.TempDir(), "missing-app-server"))
					backend.Env = []string{"PATH="}
					_, startErr := session.StartAppUI(ctx, backend, nil, nil, "", nil, false)
					if !errors.Is(startErr, os.ErrNotExist) {
						t.Errorf("report failure blocked backend launch: %v", startErr)
					}
					_, channel := shellapproval.Paths(session.FrontendDirectory)
					socket, linkErr := os.Readlink(channel)
					if !guard {
						if !errors.Is(linkErr, os.ErrNotExist) {
							t.Errorf("disabled shell guard acquired approval channel: %v", linkErr)
						}
						return
					}
					if linkErr != nil {
						t.Errorf("report failure removed approval channel: %v", linkErr)
						return
					}
					conn, dialErr := net.DialTimeout("unix", socket, time.Second)
					if dialErr != nil {
						t.Errorf("report failure disabled approval requests: %v", dialErr)
						return
					}
					conn.Close()
				}, nil)
				if err != nil || !ready {
					t.Fatalf("session setup: ready=%v err=%v", ready, err)
				}
			})
		}
	}
}
