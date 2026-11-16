package launch

import (
	"slices"
	"testing"
)

func TestEnvironmentCapabilityPreservesCallerShellPolicy(t *testing.T) {
	forwarded := []string{"exec", "-c", `shell_environment_policy.set.UNRELATED="kept"`, "-c", `shell_environment_policy.set.MEKUGI_ENVIRONMENT_FACTS="1"`, "--", "prompt"}
	args := codexArgs("http://localhost/v1", forwarded, nil)
	end := slices.Index(args, "--")
	original := slices.Index(forwarded, "--")
	if !slices.Equal(args[:original], forwarded[:original]) || !slices.Equal(args[end:], forwarded[original:]) {
		t.Fatal("caller shell policy or prompt changed")
	}

}

func TestEnvironmentCapabilityPerInvocation(t *testing.T) {
	inherited := []string{"MEKUGI_ENVIRONMENT_FACTS=1", "UNRELATED=kept", "MEKUGI_ENVIRONMENT_FACTS=other"}
	got := codexEnvironmentCapability(inherited)
	want := []string{"UNRELATED=kept", "MEKUGI_ENVIRONMENT_FACTS=1"}
	if !slices.Equal(got, want) || len(inherited) != 3 {
		t.Fatalf("capability=%q", got)
	}
}
