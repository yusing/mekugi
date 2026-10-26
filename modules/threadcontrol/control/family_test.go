package control

import "testing"

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
