package orchestrate

import (
	"encoding/json/jsontext"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordIntegrationBatchCommitsAfterBaselineSquash(t *testing.T) {
	s, source, initial := launchFixture(t)
	f := &workflowFixture{t: t, s: s, workspace: source}
	run := func(dir string, args ...string) string {
		t.Helper()
		out, err := git(t.Context(), dir, append([]string{"-c", "user.name=test", "-c", "user.email=test@example.com"}, args...)...)
		f.ok(err)
		return out
	}
	commit := func(dir, name, content string) {
		t.Helper()
		f.ok(os.WriteFile(filepath.Join(dir, name), []byte(content), 0600))
		run(dir, "add", name)
		run(dir, "commit", "-qm", content)
	}
	commit(source, "baseline", "first baseline commit")
	commit(source, "baseline", "second baseline commit")
	b, err := s.Prepare(t.Context(), source, "main", "squashed")
	f.ok(err)
	_, _, err = s.BeginLaunch(t.Context(), source, "main", "squashed", "implement", jsontext.Value(`{}`), nil, nil)
	f.ok(err)
	f.ok(s.RecordThread(t.Context(), source, "main", "squashed", "implementer"))
	commit(b.Cwd, "delta", "batch delta")
	tip := run(b.Cwd, "rev-parse", "HEAD")
	if _, err := s.RecordIntegration(t.Context(), source, "main", "squashed", "implementer"); err == nil || !strings.Contains(err.Error(), "batch commits are not integrated") {
		t.Fatal("unchanged initial baseline accepted missing batch delta", err)
	}
	// Main rewrites only its old baseline; the child's baseline remains intact.
	run(source, "reset", "--soft", initial.Base)
	run(source, "commit", "-qm", "squashed baseline")
	if _, err := s.RecordIntegration(t.Context(), source, "main", "squashed", "implementer"); err == nil || !strings.Contains(err.Error(), "batch commits are not integrated") {
		t.Fatal("squashed baseline accepted missing batch delta", err)
	}
	run(source, "cherry-pick", tip)
	copied := run(source, "rev-parse", "HEAD")
	if copied == tip {
		t.Fatal("fixture did not create a patch-equivalent copy")
	}
	proof, err := s.RecordIntegration(t.Context(), source, "main", "squashed", "implementer")
	f.ok(err)
	if proof.Tip != tip || proof.SourceTip != copied {
		t.Fatal("wrong copied-commit proof", proof)
	}
	// Reachable batch commits still pass without patch-equivalence matching.
	run(source, "reset", "--hard", tip)
	proof, err = s.RecordIntegration(t.Context(), source, "main", "squashed", "implementer")
	f.ok(err)
	if proof.Tip != tip || proof.SourceTip != tip {
		t.Fatal("wrong reachable-commit proof", proof)
	}
}
