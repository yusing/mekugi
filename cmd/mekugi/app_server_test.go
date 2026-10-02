package main

import (
	"slices"
	"strings"
	"testing"
)

func TestAppServerResumeArgv(t *testing.T) {
	for _, args := range [][]string{
		{"--yolo", "--enable", "instant_interrupt", "--high"},
		{"--yolo", "resume", "--last", "--enable", "instant_interrupt", "--high"},
		{"resume", "old-thread", "--yolo", "--enable", "instant_interrupt", "--high"},
		{"--yolo", "resume", "--enable", "instant_interrupt", "--high"},
	} {
		if _, _, err := appServerArgs(expandReasoningShortcuts(args)); err != nil {
			t.Fatal(err)
		}
		routerArgs := []string{"--debug", "--journal-compaction=auto"}
		got := appServerResumeArgv("/opt/my tools/mekugi", routerArgs, args)
		want := []string{"/opt/my tools/mekugi", "--debug", "--journal-compaction=auto", "codex", "--yolo", "--enable", "instant_interrupt", "--high"}
		if !slices.Equal(got, want) {
			t.Fatalf("resume argv for %q = %q, want %q", args, got, want)
		}
		if _, thread, err := appServerArgs(expandReasoningShortcuts(append(got[4:], "resume", "actual-thread"))); err != nil || thread != "actual-thread" {
			t.Fatalf("continuation is not accepted: %q, %v", thread, err)
		}
	}
	args := []string{"--yolo", "-m", "resume", "--config", "key='--last'", "--enable=instant_interrupt", "resume", "--last"}
	got := appServerResumeArgv("mekugi", nil, args)
	want := append([]string{"mekugi", "codex"}, args[:6]...)
	if !slices.Equal(got, want) {
		t.Fatalf("option values changed: %q, want %q", got, want)
	}
}

func TestAppServerArgs(t *testing.T) {
	for _, yolo := range []string{"--yolo", "--dangerously-bypass-approvals-and-sandbox"} {
		got, _, err := appServerArgs([]string{yolo, "-c", "features.test=true", "--config=foo=42", "--model", "model-name"})
		want := []string{"app-server", "-c", "features.test=true", "-c", "foo=42", "-c", `model="model-name"`, "-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`}
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("appServerArgs(%s) = %q, %v; want %q", yolo, got, err, want)
		}
	}
}

func TestAppServerInstantInterruptOptIn(t *testing.T) {
	for _, options := range [][]string{
		nil,
		{"--enable", "instant_interrupt"},
		{"--enable=instant_interrupt"},
		{"--disable", "instant_interrupt"},
		{"--disable=instant_interrupt"},
		{"-c", "features.instant_interrupt=true"},
		{"--config=features.instant_interrupt=false"},
		{"--enable", "instant_interrupt", "--disable", "instant_interrupt", "--enable", "code_mode"},
	} {
		for _, resume := range [][]string{nil, {"resume", "thread-id"}, {"resume", "--last"}} {
			input := append([]string{"--yolo"}, options...)
			input = append(input, resume...)
			got, _, err := appServerArgs(input)
			wantOptions := options
			if slices.Equal(options, []string{"--config=features.instant_interrupt=false"}) {
				wantOptions = []string{"-c", "features.instant_interrupt=false"}
			}
			want := append([]string{"app-server"}, wantOptions...)
			want = append(want, "-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`)
			if err != nil || !slices.Equal(got, want) {
				t.Fatalf("appServerArgs(%q) = %q, %v; want %q", input, got, err, want)
			}
			// The final wrapper must neither override the opt-in nor enable it
			// by default, including resumed launches.
			wrapped := codexArgs("http://127.0.0.1:12345/v1", got, false, false, true)
			if !slices.Equal(wrapped[:len(want)], want) {
				t.Fatalf("wrapper changed host options: %q", wrapped)
			}
			for _, arg := range wrapped[len(want):] {
				if strings.Contains(arg, "instant_interrupt") {
					t.Fatalf("wrapper imposed instant_interrupt: %q", wrapped)
				}
			}
		}
	}
}

func TestAppServerFeatureToggleMissingValue(t *testing.T) {
	for _, options := range [][]string{{"--enable"}, {"--disable"}, {"--enable="}, {"--disable="}, {"--enable", ""}, {"--disable", "--yolo"}} {
		if _, _, err := appServerArgs(append([]string{"--yolo"}, options...)); err == nil {
			t.Fatalf("accepted missing feature: %q", options)
		}
	}
}

func TestAppServerModelFlagForms(t *testing.T) {
	for _, args := range [][]string{{"-m", "gpt-6-sol"}, {"--model", "gpt-6-sol"}, {"-m=gpt-6-sol"}, {"--model=gpt-6-sol"}} {
		got, _, err := appServerArgs(append([]string{"--yolo"}, args...))
		if err != nil || !slices.Contains(got, `model="gpt-6-sol"`) {
			t.Fatalf("model option %q: %q %v", args, got, err)
		}
	}
}

func TestAppServerArgsRejectsUnmappedAndImplicitAuthorization(t *testing.T) {
	for _, args := range [][]string{nil, {"-c", `approval_policy="never"`}, {"--yolo", "--full-auto"}, {"--yolo", "prompt"}, {"--yolo", "--model"}, {"--yolo", "-c"}, {"--yolo", "--sandbox", "workspace-write"}} {
		if got, _, err := appServerArgs(args); err == nil {
			t.Errorf("appServerArgs(%q) = %q, wanted rejection", args, got)
		}
	}
}

func TestAppServerResumeArgs(t *testing.T) {
	for _, args := range [][]string{{"--yolo", "resume", "thread-id"}, {"resume", "thread-id", "--yolo", "-m", "gpt-6-sol"}} {
		got, thread, err := appServerArgs(args)
		if err != nil || thread != "thread-id" || got[0] != "app-server" || slices.Contains(got, "resume") || slices.Contains(got, thread) {
			t.Fatalf("resume launch: %q %q %v", got, thread, err)
		}
	}
	for _, args := range [][]string{{"--yolo", "--last"}, {"--yolo", "resume", "id", "--last"}, {"--yolo", "resume", "--last", "id"}, {"--yolo", "resume", "--last", "--last"}, {"resume", "--last"}, {"--yolo", "resume", ""}, {"--yolo", "resume", "id", "resume", "other"}, {"--yolo", "resume", "id", "prompt"}, {"resume", "id"}} {
		if _, _, err := appServerArgs(args); err == nil {
			t.Fatalf("accepted unsupported resume: %q", args)
		}
	}
}

func TestAppServerResumeLastArgs(t *testing.T) {
	for _, args := range [][]string{{"--yolo", "resume", "--last"}, {"resume", "--last", "--yolo", "-m", "gpt-6-sol"}, {"resume", "--yolo", "--last"}} {
		got, thread, err := appServerArgs(args)
		if err != nil || thread != "--last" || got[0] != "app-server" || slices.Contains(got, "resume") || slices.Contains(got, "--last") {
			t.Fatalf("resume last launch: %q %q %v", got, thread, err)
		}
	}
}

func TestAppServerResumePickerArgs(t *testing.T) {
	for _, args := range [][]string{{"--yolo", "resume"}, {"resume", "--yolo", "-m", "gpt-6-sol"}} {
		got, thread, err := appServerArgs(args)
		if err != nil || thread != "--pick" || got[0] != "app-server" || slices.Contains(got, "resume") {
			t.Fatalf("resume picker launch: %q %q %v", got, thread, err)
		}
	}
	if _, _, err := appServerArgs([]string{"resume"}); err == nil {
		t.Fatal("resume picker bypassed explicit --yolo")
	}
}
