package main

import (
	"slices"
	"testing"
)

func TestAppServerArgs(t *testing.T) {
	for _, yolo := range []string{"--yolo", "--dangerously-bypass-approvals-and-sandbox"} {
		got, err := appServerArgs([]string{yolo, "-c", "features.test=true", "--config=foo=42", "--model", "model-name"})
		want := []string{"app-server", "-c", "features.test=true", "-c", "foo=42", "-c", `model="model-name"`, "-c", `approval_policy="never"`, "-c", `sandbox_mode="danger-full-access"`}
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("appServerArgs(%s) = %q, %v; want %q", yolo, got, err, want)
		}
	}
}

func TestAppServerModelFlagForms(t *testing.T) {
	for _, args := range [][]string{{"-m", "gpt-6-sol"}, {"--model", "gpt-6-sol"}, {"-m=gpt-6-sol"}, {"--model=gpt-6-sol"}} {
		got, err := appServerArgs(append([]string{"--yolo"}, args...))
		if err != nil || !slices.Contains(got, `model="gpt-6-sol"`) {
			t.Fatalf("model option %q: %q %v", args, got, err)
		}
	}
}

func TestAppServerArgsRejectsUnmappedAndImplicitAuthorization(t *testing.T) {
	for _, args := range [][]string{nil, {"-c", `approval_policy="never"`}, {"--yolo", "--full-auto"}, {"--yolo", "resume"}, {"--yolo", "prompt"}, {"--yolo", "--model"}, {"--yolo", "-c"}, {"--yolo", "--sandbox", "workspace-write"}} {
		if got, err := appServerArgs(args); err == nil {
			t.Errorf("appServerArgs(%q) = %q, wanted rejection", args, got)
		}
	}
}
