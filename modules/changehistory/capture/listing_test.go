package capture

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/yusing/mekugi/libraries/commandanalysis/execscope"
)

func TestExecReconcileDoesNotClaimUnvisitedListedEntriesDeleted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	zPath := filepath.Join(root, "z")
	writeTestFile(t, zPath, "survives\n")
	info, err := os.Lstat(zPath)
	if err != nil {
		t.Fatal(err)
	}
	observation := Observation{Class: execscope.Scoped.String(), Roots: []string{root}, Listings: []Listing{{Root: root, Entries: map[string]string{"z": execFileStamp(info)}}}}
	for index := range execscope.ListingLimit + 1 {
		writeTestFile(t, filepath.Join(root, fmt.Sprintf("file-%04d.txt", index)), "new\n")
	}
	result := Reconcile(t.Context(), observation, Comparison{})
	reviews, complete := result.Files, result.Complete
	walkTruncated, added := false, 0
	for _, review := range reviews {
		walkTruncated = walkTruncated || review.Incomplete == "destination listing exceeds its bound"
		if review.BeforePath == zPath && review.AfterPath == "" {
			t.Fatalf("reconciliation falsely claims surviving z was deleted: %+v", review)
		}
		if review.Incomplete == "" && review.BeforePath == "" {
			added++
		}
	}
	if complete || !walkTruncated || added != execscope.ListingLimit {
		t.Fatalf("destination enumeration: complete=%v truncated=%v added=%d", complete, walkTruncated, added)
	}

}
