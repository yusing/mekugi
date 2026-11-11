package frontends

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/boundedcommand/run"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

func mrunStreamArguments(t testing.TB, out, errBytes int, newline bool, status int, marker, mode string) []string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{"-n", "2147483647", "--", executable, "-test.run=^TestMRunStreamProcess$", "--", "--mrun-stream-fixture", strconv.Itoa(out), strconv.Itoa(errBytes), strconv.FormatBool(newline), strconv.Itoa(status), marker, mode}
}

func TestMRunStreamProcess(t *testing.T) {
	index := slices.Index(os.Args, "--mrun-stream-fixture")
	if index < 0 {
		return
	}
	args := os.Args[index+1:]
	if args[4] != "" {
		file, err := os.OpenFile(args[4], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(24)
		}
		fmt.Fprintln(file, "executed")
		file.Close()
	}
	for index, stream := range []*os.File{os.Stdout, os.Stderr} {
		count, err := strconv.Atoi(args[index])
		if err != nil {
			os.Exit(24)
		}
		if count == 0 {
			continue
		}
		header := []string{"out\n", "err\n"}[index]
		io.WriteString(stream, header)
		value := byte('x' + index)
		if args[5] == "escaped" {
			value = 1
		}
		chunk := bytes.Repeat([]byte{value}, 32<<10)
		if args[5] == "rows" {
			for offset := 79; offset < len(chunk); offset += 80 {
				chunk[offset] = '\n'
			}
		}
		for count > 0 {
			n := min(count, len(chunk))
			if _, err := stream.Write(chunk[:n]); err != nil {
				os.Exit(24)
			}
			count -= n
		}
		if args[2] == "true" {
			fmt.Fprintln(stream)
		}
	}
	if args[5] == "cleanup" {
		entries, err := os.ReadDir(os.TempDir())
		if err != nil {
			os.Exit(24)
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "mekugi-mrun-") {
				continue
			}
			path := filepath.Join(os.TempDir(), entry.Name())
			if err := os.Rename(path, path+".held"); err != nil {
				os.Exit(24)
			}
			if err := os.Mkdir(path, 0700); err != nil {
				os.Exit(24)
			}
			if err := os.WriteFile(filepath.Join(path, "block"), nil, 0600); err != nil {
				os.Exit(24)
			}
		}
	}
	status, _ := strconv.Atoi(args[3])
	os.Exit(status)
}

func TestFrontendMRunRowsReconstructThroughMRead(t *testing.T) {
	registry := sharedTestRegistry(t)
	for _, newline := range []bool{false, true} {
		for _, tail := range []bool{false, true} {
			t.Run(fmt.Sprintf("newline=%t/tail=%t", newline, tail), func(t *testing.T) {
				t.Setenv(codexThreadIDEnvironment, uniqueTestThread("mrun-rows"))
				spools := t.TempDir()
				t.Setenv("TMPDIR", spools)
				marker := filepath.Join(t.TempDir(), "executions")
				args := mrunStreamArguments(t, 90000, 70000, newline, 23, marker, "")
				if tail {
					args = append([]string{"--tail"}, args...)
				}
				var stdout, stderr bytes.Buffer
				checkedWrite := deliveryWriter(func(p []byte) (int, error) {
					entries, err := os.ReadDir(spools)
					if err != nil || len(entries) != 0 {
						t.Fatalf("spools survived through delivery: %v, %v", entries, err)
					}
					return stdout.Write(p)
				})
				handled, status := runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), testCommands(), t.Context(), filepath.Join(registry.FrontendDirectory, "mrun"), args, nil, checkedWrite, &stderr)
				_, next, ok := strings.Cut(stderr.String(), "next_call: mread ")
				if !handled || status != 23 || stdout.String() != "out\n" || !ok || !strings.HasPrefix(stderr.String(), "err\n") {
					t.Fatalf("initial page: status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
				}
				handle := strings.Fields(next)[0]
				for index, stream := range []string{"stdout", "stderr"} {
					var reconstructed strings.Builder
					args = []string{handle, "--" + stream, "--max-tokens", "15500"}
					for page := 0; ; page++ {
						if page > 12 {
							t.Fatal("mread did not advance")
						}
						stdout.Reset()
						stderr.Reset()
						_, status = runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), testCommands(), t.Context(), filepath.Join(registry.FrontendDirectory, "mread"), args, nil, &stdout, &stderr)
						body := stdout.String()
						if stream == "stderr" {
							var framed bool
							body, framed = strings.CutPrefix(body, "[stderr bytes]\n")
							if !framed {
								t.Fatalf("lost byte-paging frame: %q", body[:min(100, len(body))])
							}
							body, framed = strings.CutSuffix(body, "\n[/stderr]\n")
							if !framed {
								t.Fatal("missing stderr closing frame")
							}
						}
						reconstructed.WriteString(body)
						_, next, more := strings.Cut(stderr.String(), "next_call: mread ")
						if !more {
							if status != 0 {
								t.Fatalf("mread failed: %s", stderr.String())
							}
							break
						}
						next, _, _ = strings.Cut(next, "\n")
						args = strings.Fields(next)
					}
					want := strings.Repeat(string(byte('x'+index)), []int{90000, 70000}[index])
					if newline {
						want += "\n"
					}
					if reconstructed.String() != want {
						t.Fatalf("%s reconstructed bytes=%d, want=%d", stream, reconstructed.Len(), len(want))
					}
				}
				data, err := os.ReadFile(marker)
				if err != nil || string(data) != "executed\n" {
					t.Fatalf("producer reran: %q %v", data, err)
				}
			})
		}
	}
}

func TestFrontendMRunCapacityAndCleanupFailures(t *testing.T) {
	registry := sharedTestRegistry(t)
	for _, tc := range []struct {
		mode    string
		count   int
		failure string
	}{
		{"", recovery.MaxOutputBytes + 1, "saved read output"},
		{"escaped", 6 << 20, "encoded read record"},
		{"cleanup", 90000, "capture cleanup failed"},
	} {
		for _, originalStatus := range []int{0, 23} {
			t.Run(fmt.Sprintf("%s/%d", tc.failure, originalStatus), func(t *testing.T) {
				t.Setenv(codexThreadIDEnvironment, uniqueTestThread("mrun-failure"))
				spools := t.TempDir()
				t.Setenv("TMPDIR", spools)
				marker := filepath.Join(t.TempDir(), "executions")
				args := mrunStreamArguments(t, tc.count, 0, false, originalStatus, marker, tc.mode)
				var stdout, stderr bytes.Buffer
				_, status := runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), testCommands(), t.Context(), filepath.Join(registry.FrontendDirectory, "mrun"), args, nil, &stdout, &stderr)
				wantStatus := originalStatus
				if wantStatus == 0 {
					wantStatus = 1
				}
				if status != wantStatus || stdout.String() != "out\n" || !strings.Contains(stderr.String(), tc.failure) {
					t.Fatalf("status=%d stdout=%q stderr=%q", status, stdout.String(), stderr.String())
				}
				if strings.Contains(stderr.String(), "next_call") != (tc.mode == "cleanup") || strings.Contains(stderr.String(), "recovery unavailable") == (tc.mode == "cleanup") {
					t.Fatalf("failure misclassified: %s", stderr.String())
				}
				if tc.mode == "cleanup" {
					_, next, _ := strings.Cut(stderr.String(), "next_call: mread ")
					stdout.Reset()
					stderr.Reset()
					_, status := runToolPluginWorkerInHostProcess(testHistory(), testRecovery(), testCommands(), t.Context(), filepath.Join(registry.FrontendDirectory, "mread"), []string{strings.Fields(next)[0]}, nil, &stdout, &stderr)
					if status != 1 || stdout.Len() == 0 || strings.Trim(stdout.String(), "x") != "" || !strings.Contains(stderr.String(), "next_call") {
						t.Fatalf("cleanup invalidated published recovery: status=%d stderr=%q", status, stderr.String())
					}
				}
				entries, err := os.ReadDir(spools)
				if err != nil || tc.mode != "cleanup" && len(entries) != 0 {
					t.Fatalf("unreleased spools: %v %v", entries, err)
				}
				data, err := os.ReadFile(marker)
				if err != nil || string(data) != "executed\n" {
					t.Fatalf("producer reran: %q %v", data, err)
				}
			})
		}
	}
}

func BenchmarkMRunRowFinalizationAndPublication(b *testing.B) {
	for _, tc := range []struct {
		count int
		mode  string
	}{
		{8 << 20, ""}, {16 << 20, ""}, {32 << 20, ""}, {128 << 20, ""},
		{32 << 20, "rows"}, {128 << 20, "rows"}, {6 << 20, "escaped"}, {16 << 20, "escaped"},
	} {
		b.Run(fmt.Sprintf("MiB=%d/mode=%s", tc.count>>20, tc.mode), func(b *testing.B) {
			service := testHistory()
			directory := b.TempDir()
			if _, err := service.Open(b.Context(), directory); err != nil {
				b.Fatal(err)
			}
			selected := scope.Retained{Directory: directory}
			recovered := testRecovery()
			// Warm fixed tokenizer/encoder machinery before measuring scaling.
			warm, err := (run.Service{}).Execute(b.Context(), command.Request{Args: mrunStreamArguments(b, 100000, 0, false, 0, "", "")})
			if err != nil {
				b.Fatal(err)
			}
			if _, err := retainExecutionOutput(b.Context(), recovered, selected, warm); err != nil {
				b.Fatal(err)
			}
			args := append([]string{"--tail"}, mrunStreamArguments(b, tc.count, 0, false, 0, "", tc.mode)...)
			runtime.GC()
			var stats runtime.MemStats
			runtime.ReadMemStats(&stats)
			baseline := stats.HeapAlloc
			var peak atomic.Uint64
			peak.Store(baseline)
			stop, done := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(done)
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-stop:
						return
					case <-ticker.C:
					}
					var sample runtime.MemStats
					runtime.ReadMemStats(&sample)
					if sample.HeapAlloc > peak.Load() {
						peak.Store(sample.HeapAlloc)
					}
				}
			}()
			b.ReportAllocs()
			for b.Loop() {
				output, err := (run.Service{}).Execute(b.Context(), command.Request{Args: args})
				if err != nil {
					b.Fatal(err)
				}
				_, err = retainExecutionOutput(b.Context(), recovered, selected, output)
				if (err != nil) != (tc.count > recovery.MaxOutputBytes || tc.mode == "escaped") {
					b.Fatalf("publication=%v", err)
				}
			}
			close(stop)
			<-done
			b.ReportMetric(float64(peak.Load()-baseline), "sampled-peak-heap-B")
			b.ReportMetric(float64(tc.count+4), "capture-disk-B/op")
		})
	}
}

func TestFrontendMRunPureCapacityFailureNeedsNoStore(t *testing.T) {
	args := mrunStreamArguments(t, recovery.MaxOutputBytes+1, 0, false, 23, "", "")
	got, err := (run.Service{}).Execute(t.Context(), command.Request{Args: args})
	if err != nil || got.Delivery == nil {
		t.Fatalf("missing failure plan: %v", err)
	}
	for range 2 {
		prepared, err := retainExecutionOutput(context.Background(), nil, scope.Retained{}, got)
		if err == nil || !strings.Contains(err.Error(), "saved read output") || prepared.Stdout != "out\n" || prepared.ExitCode != 23 || prepared.Delivery != nil || prepared.OmittedOutput != nil {
			t.Fatalf("preflight needed storage: %+v %v", prepared, err)
		}
	}
}
