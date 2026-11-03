package capture

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestCreatedTemporaryTreeKeepsBoundedGapAndNamedWrites(t *testing.T) {
	for _, count := range []int{2, 300} {
		root := t.TempDir()
		observation := Observation{}
		named := filepath.Join(root, "named.txt")
		observation.Files = append(observation.Files, File{Path: named})
		for i := range count {
			writeTestFile(t, filepath.Join(root, fmt.Sprintf("file-%03d", i)), "extracted\n")
		}
		writeTestFile(t, named, "authored\n")
		observation.AddCreatedPath(root, true)
		result := Reconcile(t.Context(), observation, Comparison{})
		if count == 2 {
			if !result.Complete || len(result.Files) != count+1 {
				t.Fatalf("small temporary tree lost evidence: %+v", result)
			}
		} else if result.Complete || len(result.Files) != 2 || len(observation.Listings) != 0 || len(observation.Files) != 1 {
			t.Fatalf("large temporary tree retained extracted content: %+v, %+v", observation, result)
		}
		if result.Files[0].AfterPath != named || !strings.Contains(result.Files[0].Diff, "+authored") {
			t.Fatalf("explicit temporary write lost: %+v", result.Files)
		}
	}
}

func TestTemporaryTreeEnumerationAllocationsStayBounded(t *testing.T) {
	var allocations []float64
	for _, count := range []int{300, 3000} {
		root := t.TempDir()
		for i := range count {
			writeTestFile(t, filepath.Join(root, fmt.Sprintf("file-%04d", i)), "")
		}
		allocations = append(allocations, testing.AllocsPerRun(1, func() {
			observation := Observation{}
			observation.AddCreatedPath(root, true)
			if len(observation.Omitted) != 1 || len(observation.Listings) != 0 {
				t.Fatalf("wide temporary tree was not bounded: %+v", observation)
			}
		}))
	}
	if allocations[1] > 2*allocations[0] {
		t.Fatalf("directory admission allocations grew with tree size: %v", allocations)
	}
}

func TestExecObservationBoundsEncodedCapture(t *testing.T) {
	workspace := t.TempDir()
	// JSON escapes control bytes as six bytes, so this content encodes past
	// the bound.
	writeTestFile(t, filepath.Join(workspace, "page.html"), strings.Repeat("\x01", 2<<20))
	writeTestFile(t, filepath.Join(workspace, "small"), "kept\n")
	// Exercise encoded-size admission independently of the forwarding deadline.
	// Race instrumentation can make this deliberately large fixture exceed it.
	observation, observed := captureExecObservationWithin([]commandanalysis.Command{{
		Command: "touch page.html small", Workdir: workspace, Shell: "bash",
	}}, false, false, Options{})
	if !observed || observation == nil || len(observation.Files) != 2 {
		t.Fatalf("named files were not captured: %+v", observation)
	}
	if size := len(responses.MustMarshalJSON(observation)); size > maxExecObservationBytes {
		t.Fatalf("encoded capture is %d bytes", size)
	}
	for _, file := range observation.Files {
		if strings.HasSuffix(file.Path, "page.html") && (file.Content != "" || file.Stamp == "") {
			t.Fatalf("large file kept its content: %d bytes", len(file.Content))
		}
		if strings.HasSuffix(file.Path, "small") && file.Content != "kept\n" {
			t.Fatalf("small file lost its content: %+v", file)
		}
	}
	if result := Reconcile(t.Context(), *observation, Comparison{}); !result.Complete || len(result.Files) != 0 {
		t.Fatalf("unchanged-file reconciliation: complete=%t reviews=%+v", result.Complete, result.Files)
	}
}
