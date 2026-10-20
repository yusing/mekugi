package shellapproval

import "testing"

func TestPaths(t *testing.T) {
	directory, channel := Paths("/run/mekugi-tools-1/bin")
	if directory != "/run/mekugi-tools-1/vcs-guard" || channel != "/run/mekugi-tools-1/vcs-approval.sock" || ChannelOf(directory) != channel {
		t.Fatalf("paths = %q %q", directory, channel)
	}
}
