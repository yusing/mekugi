package router

import (
	"encoding/json/jsontext"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"

	json "encoding/json/v2"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/backends/codex/shellapproval"
)

func TestShellApprovalCompositionSocket(t *testing.T) {
	for _, selected := range []struct {
		name         string
		remote, sudo bool
	}{
		{name: "none"},
		{name: "remote", remote: true},
		{name: "sudo", sudo: true},
		{name: "both", remote: true, sudo: true},
	} {
		t.Run(selected.name, func(t *testing.T) {
			t.Parallel()
			var guards []binding.ShellApproval
			for _, guard := range testGuards() {
				if guard.Kind == binding.RemoteWrite && selected.remote || guard.Kind == binding.Sudo && selected.sudo {
					guards = append(guards, guard)
				}
			}
			path := filepath.Join(t.TempDir(), "approval.sock")
			server, err := sessioncontrol.ListenShellApprovals(t.Context(), path, 5*time.Second, guards)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(server.Close)
			for _, request := range []struct {
				name, kind string
				active     bool
			}{
				{name: "remote", active: selected.remote},
				{name: "sudo", kind: "sudo", active: selected.sudo},
				{name: "password", kind: "sudo-password", active: selected.sudo},
				{name: "unknown", kind: "unknown"},
			} {
				t.Run(request.name, func(t *testing.T) {
					u, _ := newAppServerTestUI()
					u.approvals.grants = sessioncontrol.NewApprovalGrants(guards)
					conn, err := net.DialTimeout("unix", path, 5*time.Second)
					if err != nil {
						t.Fatal(err)
					}
					defer conn.Close()
					if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
						t.Fatal(err)
					}
					message := shellapproval.Message{Kind: request.kind, Cwd: "/work", Executable: "/bin/tool", Argv: []string{"tool", "publish"}}
					if err := json.MarshalWrite(conn, &message); err != nil {
						t.Fatal(err)
					}
					type result struct {
						reply shellapproval.Reply
						err   error
					}
					answers := make(chan result, 1)
					go func() {
						var got result
						got.err = json.UnmarshalDecode(jsontext.NewDecoder(conn), &got.reply)
						answers <- got
					}()
					select {
					case pending := <-server.Requests():
						if !request.active {
							t.Fatal("inactive kind reached the UI request channel")
						}
						if pending.Kind != message.Kind || pending.Executable != message.Executable {
							t.Fatal("socket changed the approval identity")
						}
						u.addGuardApproval(pending)
						if request.kind == "sudo-password" {
							if len(u.questionCalls()) != 1 || len(u.pendingApprovals()) != 0 {
								t.Fatal("sudo password did not reach its secret-entry UI")
							}
							questionTestPaint(t, u, 80)
							appServerTestKeys(t, u, "fixture-password\r")
						} else {
							if len(u.pendingApprovals()) != 1 || len(u.questionCalls()) != 0 {
								t.Fatal("selected command did not reach approval UI")
							}
							questionTestPaint(t, u, 80)
							appServerTestKeys(t, u, "3\r")
						}
					case got := <-answers:
						if got.err != nil || request.active || got.reply.OK || got.reply.Password != "" || got.reply.Reason == "" {
							t.Fatalf("unexpected unprompted reply: %+v, err=%v", got.reply, got.err)
						}
						// Exercise the UI's own absence boundary independently of the socket.
						pending := compositionApproval(request.kind)
						u.addGuardApproval(pending)
						select {
						case denied := <-pending.Reply:
							if denied.OK || denied.Password != "" || denied.Reason == "" {
								t.Fatalf("inactive UI reply = %+v", denied)
							}
						default:
							t.Fatal("inactive UI request was not denied")
						}
						if len(u.pendingApprovals()) != 0 || len(u.questionCalls()) != 0 {
							t.Fatal("inactive kind opened a prompt")
						}
						return
					case <-time.After(5 * time.Second):
						t.Fatal("socket neither replied nor requested approval")
					}
					got := <-answers
					if got.err != nil {
						t.Fatal(got.err)
					}
					if request.kind == "sudo-password" {
						if !got.reply.OK || got.reply.Password != "fixture-password" {
							t.Fatal("password answer did not reach the socket client")
						}
					} else if got.reply.OK || got.reply.Reason == "" {
						t.Fatalf("command denial did not reach socket client: %+v", got.reply)
					}
				})
			}
		})
	}
}

func compositionApproval(kind string) *sessioncontrol.ShellApprovalRequest {
	return &sessioncontrol.ShellApprovalRequest{Kind: kind, Cwd: "/work", Executable: "/bin/tool", Argv: []string{"tool", "publish"}, Reply: make(chan shellapproval.Reply, 1), Done: make(chan struct{})}
}

func TestShellApprovalCompositionGrantIsolation(t *testing.T) {
	for _, kind := range []string{"", "sudo"} {
		t.Run("from-"+kind, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			first := compositionApproval(kind)
			u.addGuardApproval(first)
			questionTestPaint(t, u, 80)
			appServerTestKeys(t, u, "2\r")
			if len(first.Reply) != 1 || !(<-first.Reply).OK {
				t.Fatal("session grant was not accepted")
			}
			repeated := compositionApproval(kind)
			u.addGuardApproval(repeated)
			if len(repeated.Reply) != 1 || !(<-repeated.Reply).OK || len(u.pendingApprovals()) != 0 {
				t.Fatal("selected module did not reuse its own grant")
			}
			otherKind := "sudo"
			if kind == "sudo" {
				otherKind = ""
			}
			other := compositionApproval(otherKind)
			u.addGuardApproval(other)
			if len(other.Reply) != 0 || len(u.pendingApprovals()) != 1 {
				t.Fatal("session grant crossed module kinds for the same command identity")
			}
			fresh, _ := newAppServerTestUI()
			newSession := compositionApproval(kind)
			fresh.addGuardApproval(newSession)
			if len(newSession.Reply) != 0 || len(fresh.pendingApprovals()) != 1 {
				t.Fatal("session grant crossed fresh UI instances")
			}
		})
	}
}
