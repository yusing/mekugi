package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/shellapproval"
	"github.com/yusing/mekugi/libraries/shellinstrumentation"
	segmenttransport "github.com/yusing/mekugi/modules/commandsegments/transport"
)

func TestFrontendShellEnvironmentSelectedGuards(t *testing.T) {
	bash, err := frontendFixtureShell("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	for _, remote := range []bool{false, true} {
		for _, sudo := range []bool{false, true} {
			t.Run(fmt.Sprintf("remote=%v/sudo=%v", remote, sudo), func(t *testing.T) {
				root := t.TempDir()
				frontend, real, helper := filepath.Join(root, "bin"), filepath.Join(root, "real"), filepath.Join(root, "mekugi-exec")
				for _, directory := range []string{frontend, real} {
					if err := os.Mkdir(directory, 0700); err != nil {
						t.Fatal(err)
					}
				}
				for path, body := range map[string]string{
					helper:                      "printf 'guard:%s\n' \"${0##*/}\"",
					filepath.Join(real, "git"):  "printf 'native:git\n'",
					filepath.Join(real, "sudo"): "printf 'native:sudo\n'",
				} {
					if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				var guards []binding.ShellApproval
				want := "native:git\nnative:sudo\n"
				if remote {
					guards = append(guards, binding.ShellApproval{Kind: binding.RemoteWrite})
					want = strings.Replace(want, "native:git", "guard:git", 1)
				}
				if sudo {
					guards = append(guards, binding.ShellApproval{Kind: binding.Sudo})
					want = strings.Replace(want, "native:sudo", "guard:sudo", 1)
				}
				environment, err := frontendShellEnvironment([]string{"PATH=" + real + ":/bin:/usr/bin"}, frontend, helper, guards, segmenttransport.Endpoints{})
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.CommandContext(t.Context(), bash, "-c", "command git push; command sudo -n probe")
				cmd.Env = environment
				output, err := cmd.CombinedOutput()
				if err != nil || string(output) != want {
					t.Fatalf("selected guards output=%q err=%v, want %q", output, err, want)
				}
				vcs, _ := shellapproval.Paths(frontend)
				for directory, enabled := range map[string]bool{vcs: remote, shellapproval.SudoDirectory(frontend): sudo} {
					_, err := os.Stat(directory)
					if enabled && err != nil || !enabled && !os.IsNotExist(err) {
						t.Fatalf("resource %s enabled=%v err=%v", directory, enabled, err)
					}
				}
				if remote || sudo {
					directory := vcs
					if !remote {
						directory = shellapproval.SudoDirectory(frontend)
					}
					for _, shell := range shellinstrumentation.Shells {
						if target, err := os.Readlink(filepath.Join(directory, shell)); err != nil || target != helper {
							t.Fatalf("shared shell %s: target=%s err=%v", shell, target, err)
						}
					}
				}
			})
		}
	}
}
