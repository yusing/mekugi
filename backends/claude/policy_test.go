package claude

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestPolicyApprovalModes(t *testing.T) {
	for _, tc := range []struct{ mode, reviewer string }{
		{"never", "user"}, {"never", "auto_review"}, {"on-request", "user"}, {"untrusted", "user"},
	} {
		t.Run(tc.mode+"/"+tc.reviewer, func(t *testing.T) {
			mode := tc.mode
			p, err := CompilePolicy(jsontext.Value(`"`+mode+`"`), tc.reviewer, false)
			if err != nil {
				t.Fatal(err)
			}
			var options struct {
				PermissionMode                  string `json:"permissionMode"`
				AllowDangerouslySkipPermissions bool   `json:"allowDangerouslySkipPermissions"`
				Sandbox                         struct {
					Enabled *bool `json:"enabled"`
				} `json:"sandbox"`
				Tools []string `json:"tools"`
			}
			if err := json.Unmarshal(p.Options(), &options); err != nil {
				t.Fatal(err)
			}
			wantPermission := "default"
			if mode == "never" {
				wantPermission = "bypassPermissions"
			}
			if options.PermissionMode != wantPermission || options.AllowDangerouslySkipPermissions != (mode == "never") || options.Sandbox.Enabled == nil || *options.Sandbox.Enabled {
				t.Fatalf("unexpected SDK options: %s", p.Options())
			}
			if len(options.Tools) == 0 {
				t.Fatal("native tools missing")
			}
			for _, tool := range options.Tools {
				d, err := p.CheckTool(tool, jsontext.Value(`{"command":"echo test"}`))
				if err != nil {
					t.Fatal(err)
				}
				if mode == "untrusted" && IsShellTool(tool) {
					if !d.Ask || d.Allow || d.Reason == "" {
						t.Fatalf("untrusted shell did not require approval: %+v", d)
					}
				} else if d != (Decision{}) {
					t.Fatalf("%s bypassed SDK permission routing: %+v", tool, d)
				}
			}
			prompt := p.CheckApproval()
			if prompt.Ask != (mode != "never") || prompt.Allow || prompt.Reason == "" {
				t.Fatalf("native permission prompt: %+v", prompt)
			}
			copy := p.Options()
			copy[0] = '!'
			if !p.Options().IsValid() {
				t.Fatal("caller mutated retained SDK options")
			}
		})
	}
}

func TestPolicyNeverBundledSDKOptions(t *testing.T) {
	if _, err := bundle.ReadFile("dist/worker.mjs"); err != nil {
		t.Skip("optional Claude SDK bundle has not been built")
	}
	p, err := CompilePolicy(jsontext.Value(`"never"`), "auto_review", false)
	if err != nil {
		t.Fatal(err)
	}
	cfg := fixtureConfig(t)
	cfg.Options = p.Options()
	cfg.ClaudePath, err = filepath.Abs("testdata/mock-claude.mjs")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	c, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_, err = c.Request(ctx, "input", jsontext.Value(`{"type":"user","uuid":"c28dc5b5-9745-4ee0-a9b5-0a384bcb657a","parent_tool_use_id":null,"shouldQuery":false,"message":{"role":"user","content":"retained"}}`))
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case raw, open := <-c.Events():
			if !open {
				t.Fatalf("SDK ended: %v; %s", c.Err(), c.Diagnostics())
			}
			var event struct {
				Type string   `json:"type"`
				Args []string `json:"fixture_args"`
			}
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type != "system" {
				continue
			}
			if !slices.Contains(event.Args, "--permission-mode=bypassPermissions") || !slices.Contains(event.Args, "--allow-dangerously-skip-permissions") {
				t.Fatalf("SDK did not receive never approval bypass: %v", event.Args)
			}
			return
		case <-ctx.Done():
			t.Fatalf("SDK option consumption: %v; %s", ctx.Err(), c.Diagnostics())
		}
	}
}

func TestPolicyRejectsUnsupportedAdmission(t *testing.T) {
	for _, raw := range []string{
		`null`, `true`, `{}`, `"unknown"`, `"on-failure"`,
		`{"granular":{}}`,
		`{"granular":{"sandbox_approval":null,"rules":true,"mcp_elicitations":true}}`,
		`{"granular":{"sandbox_approval":true,"rules":true,"mcp_elicitations":true,"future_flag":true}}`,
		`{"granular":{"sandbox_approval":true,"rules":true,"mcp_elicitations":true},"extra":true}`,
	} {
		if _, err := CompilePolicy(jsontext.Value(raw), "user", false); err == nil {
			t.Errorf("admitted unsupported policy %s", raw)
		}
	}
	for _, tc := range []struct{ approval, reviewer string }{
		{`"never"`, "unknown"},
		{`"on-request"`, "auto_review"},
		{`"untrusted"`, "auto_review"},
		{`{"granular":{"sandbox_approval":true,"rules":true,"mcp_elicitations":true}}`, "auto_review"},
	} {
		if _, err := CompilePolicy(jsontext.Value(tc.approval), tc.reviewer, false); err == nil {
			t.Errorf("unsupported reviewer %q admitted for %s", tc.reviewer, tc.approval)
		}
	}
}

func TestPolicyNativeToolBoundary(t *testing.T) {
	p, err := CompilePolicy(jsontext.Value(`"on-request"`), "user", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Agent", "Task", "Skill", "UnknownTool", "mcp__external__execute"} {
		d, err := p.CheckTool(name, jsontext.Value(`{}`))
		if err != nil || d.Allow || d.Ask || d.Reason == "" {
			t.Errorf("unsupported tool %q: %+v, %v", name, d, err)
		}
	}
	d, err := p.CheckTool("mcp__threadcontrol__send_message", jsontext.Value(`{}`))
	if err != nil || !d.Allow || d.Ask {
		t.Fatalf("worker-authenticated coordination: %+v, %v", d, err)
	}
	for _, raw := range []string{`null`, `[]`, `{"broken":`, `{"duplicate":1,"duplicate":2}`} {
		if _, err := p.CheckTool("Bash", jsontext.Value(raw)); err == nil {
			t.Errorf("admitted invalid tool input %s", raw)
		}
	}
}

func TestPolicyNativeSkillsFollowSkillManager(t *testing.T) {
	for _, nativeSkills := range []bool{false, true} {
		p, err := CompilePolicy(jsontext.Value(`"on-request"`), "user", nativeSkills)
		if err != nil {
			t.Fatal(err)
		}
		var options struct {
			Tools []string `json:"tools"`
		}
		if err := json.Unmarshal(p.Options(), &options); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(options.Tools, "Skill") != nativeSkills {
			t.Fatalf("native skills %v exposed tools %v", nativeSkills, options.Tools)
		}
		d, err := p.CheckTool("Skill", jsontext.Value(`{"skill":"fixture"}`))
		if err != nil || (d == Decision{}) != nativeSkills {
			t.Fatalf("native skills %v Skill admission: %+v %v", nativeSkills, d, err)
		}
	}
}
