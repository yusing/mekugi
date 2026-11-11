package run

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

func writeMRunTestBytes(t *testing.T, capture *mrunRowCapture, count int, value byte) {
	t.Helper()
	chunk := bytes.Repeat([]byte{value}, 32<<10)
	for count > 0 {
		n := min(count, len(chunk))
		if written, err := capture.Write(chunk[:n]); err != nil || written != n {
			t.Fatalf("write=%d err=%v", written, err)
		}
		count -= n
	}
}

func TestMRunRowCapacityAfterDeliveredPrefixes(t *testing.T) {
	for _, tail := range []bool{false, true} {
		for _, delta := range []int{-1, 0, 1} {
			out, err := newMRunRowCapture(int(^uint(0)>>1), tail)
			if err != nil {
				t.Fatal(err)
			}
			errCapture, err := newMRunRowCapture(int(^uint(0)>>1), tail)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { out.close(); errCapture.close() })
			out.Write([]byte("out\n"))
			errCapture.Write([]byte("err\n"))
			writeMRunTestBytes(t, out, recovery.MaxOutputBytes/2, 'a')
			writeMRunTestBytes(t, errCapture, recovery.MaxOutputBytes/2+delta, 'b')
			got, err := finalizeMRunRows(t.Context(), out, errCapture, 23)
			if err != nil || got.Stdout != "out\n" || got.Stderr != "err\n" || got.ExitCode != 23 {
				t.Fatalf("tail=%t delta=%d: admitted output changed: err=%v stdout=%q stderr=%q status=%d", tail, delta, err, got.Stdout, got.Stderr, got.ExitCode)
			}
			if delta > 0 {
				if got.OmittedOutput != nil || got.Delivery == nil || got.Delivery.ReferenceCount() != 0 {
					t.Fatal("oversized omissions were materialized or reserved references")
				}
				_, first := got.Delivery.Prepare(nil)
				_, second := got.Delivery.Prepare(nil)
				if first == nil || first != second || !strings.Contains(first.Error(), "saved read output") {
					t.Fatalf("preflight is not pure: %v, %v", first, second)
				}
			} else if got.Delivery != nil || got.OmittedOutput == nil || len(got.OmittedOutput.Stdout)+len(got.OmittedOutput.Stderr) != recovery.MaxOutputBytes+delta {
				t.Fatal("capacity must count only normalized omitted bytes after both delivered prefixes")
			}
		}
	}
}

func TestMRunRowCapacityCountsNormalizedBytes(t *testing.T) {
	for _, tc := range []struct {
		value byte
		count int
	}{
		{0xff, recovery.MaxOutputBytes + 1}, // one malformed run becomes one replacement
		{0x80, recovery.MaxOutputBytes + 1},
	} {
		out, err := newMRunRowCapture(1, false)
		if err != nil {
			t.Fatal(err)
		}
		errCapture, err := newMRunRowCapture(1, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { out.close(); errCapture.close() })
		writeMRunTestBytes(t, out, tc.count, tc.value)
		got, err := finalizeMRunRows(t.Context(), out, errCapture, 0)
		if err != nil || got.Stdout != "\uFFFD" || got.OmittedOutput != nil || got.Delivery != nil {
			t.Fatalf("normalized delivery=%+v err=%v", got, err)
		}
	}
}

func TestMRunRowCapacityRejectsExpandedMalformedBytes(t *testing.T) {
	out, err := newMRunRowCapture(2, false)
	if err != nil {
		t.Fatal(err)
	}
	errCapture, err := newMRunRowCapture(2, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { out.close(); errCapture.close() })
	out.Write([]byte("out\n"))
	chunk := bytes.Repeat([]byte{0xff, 'a'}, 8192)
	for range recovery.MaxOutputBytes / 4 / 8192 {
		out.Write(chunk)
	}
	out.Write([]byte{0xff, 'a'})
	got, err := finalizeMRunRows(t.Context(), out, errCapture, 0)
	if err != nil || got.Stdout != "out\n" || got.Delivery == nil || got.OmittedOutput != nil {
		t.Fatalf("expanded bytes bypassed capacity: %+v %v", got, err)
	}
}

func TestMRunRowCaptureFailuresStillDrainAndCleanUp(t *testing.T) {
	out, err := newMRunRowCapture(1, true)
	if err != nil {
		t.Fatal(err)
	}
	path := out.file.Name()
	out.Write([]byte("first\n"))
	if err := out.file.Close(); err != nil {
		t.Fatal(err)
	}
	if n, err := out.Write([]byte("row\n")); n != 4 || err != nil || out.err == nil {
		t.Fatalf("capture error failed to drain: n=%d err=%v capture=%v", n, err, out.err)
	}
	if _, err := out.selectRows(t.Context()); err == nil {
		t.Fatal("closed spool produced a verified selection")
	}
	if err := out.close(); err == nil {
		t.Fatal("close failure was hidden")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed capture was not removed: %v", err)
	}

	out, err = newMRunRowCapture(1, true)
	if err != nil {
		t.Fatal(err)
	}
	out.Write([]byte("row\n"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := out.selectRows(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	if err := out.close(); err != nil {
		t.Fatal(err)
	}
}

// A fresh process ensures a previous selection cannot warm the tokenizer and
// hide its initialization cost on ordinary byte-proven fitting commands.
func TestMRunSmallRowsAvoidColdTokenizer(t *testing.T) {
	const marker = "--mrun-cold-small"
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	index := slices.Index(os.Args, marker)
	if index < 0 {
		child := exec.CommandContext(t.Context(), executable, "-test.run=^TestMRunSmallRowsAvoidColdTokenizer$", "--", marker, "measure")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("cold row capture: %v\n%s", err, output)
		}
		return
	}
	if os.Args[index+1] == "empty" {
		os.Exit(0)
	}
	args := []string{"-n", "1", "--", executable, "-test.run=^TestMRunSmallRowsAvoidColdTokenizer$", "--", marker, "empty"}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got, err := (Service{}).Execute(t.Context(), command.Request{Args: args})
	runtime.ReadMemStats(&after)
	if err != nil || got.ExitCode != 0 || got.Stdout != "" || got.Stderr != "" {
		t.Fatalf("empty command output=%+v err=%v", got, err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 2<<20 {
		t.Fatalf("small row output initialized expensive machinery: %d bytes", allocated)
	}
}
