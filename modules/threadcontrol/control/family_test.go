package control

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestFamilyMetadataUsesLogicalRootAndDetachedMembers(t *testing.T) {
	s, _ := fixture(t)
	child(t, s, "root", "worker", "/root/worker", "wt")
	child(t, s, "worker", "nested", "/root/worker/nested", "nt")
	for _, thread := range []string{"root", "worker", "nested"} {
		root, members := s.Family(thread)
		if root != "root" || len(members) != 3 || members[0].Thread != "root" || members[1].Parent != "root" || members[2].Parent != "worker" {
			t.Fatalf("%s family = %q, %+v", thread, root, members)
		}
		members[0].Task = "changed"
		_, next := s.Family(thread)
		if next[0].Task != "/root" {
			t.Fatal("navigation changed coordination metadata")
		}
	}
	if root, members := s.Family("unknown"); root != "" || members != nil {
		t.Fatal("unknown thread acquired family metadata")
	}
}

func TestFamilyNonAttachableCapabilitySurvivesRestore(t *testing.T) {
	s, session := fixture(t)
	child(t, s, "root", "claude", "/root/worker", "ct")
	child(t, s, "root", "codex", "/root/claude-name", "dt")
	must(t, s.update(t.Context(), "claude", func(_ *Tree, member *Member) error {
		member.Settings.NonAttachable = true
		return nil
	}))
	must(t, s.AdmitNamespace(t.Context(), "claude"))
	checkpoint := filepath.Join(session.Store.Directory(), recordName("root"))
	before, err := os.ReadFile(checkpoint)
	must(t, err)
	for _, selected := range []string{"root", "claude"} {
		members, size, err := ReadFamily(session.Store, selected)
		must(t, err)
		if len(members) != 3 || size != len(before) {
			t.Fatalf("retained family %s: %+v, bytes=%d", selected, members, size)
		}
		for _, member := range members {
			if member.NonAttachable != (member.Thread == "claude") || member.Status != Working {
				t.Fatalf("retained read changed capabilities or resumed work: %+v", member)
			}
		}
	}
	if members, _, err := ReadFamily(session.Store, "unrelated"); err != nil || members != nil {
		t.Fatalf("unrelated namespace acquired family: %+v, %v", members, err)
	}
	after, err := os.ReadFile(checkpoint)
	must(t, err)
	if !bytes.Equal(before, after) {
		t.Fatal("retained family read rewrote the checkpoint")
	}
	restored := New()
	must(t, restored.Restore(t.Context(), session))
	for _, service := range []*Service{s, restored} {
		_, members := service.Family("root")
		for _, member := range append(members, service.Descendants("root")...) {
			if member.NonAttachable != (member.Thread == "claude") {
				t.Fatalf("backend capability changed for %+v", member)
			}
		}
	}
}
