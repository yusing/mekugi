package rollout

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRuntimeRequiresExactTurnAndCompleteAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	data := `{"type":"session_meta","payload":{"id":"thread"}}
{"type":"turn_context","payload":{"turn_id":"selected","cwd":"/work","model":"actual","effort":"high","approval_policy":"never","approvals_reviewer":"user","permission_profile":{"type":"managed","file_system":{"type":"restricted","entries":[{"path":{"type":"path","path":"/denied"},"access":"deny"}]},"network":"restricted"}}}
{"type":"turn_context","payload":{"turn_id":"later","cwd":"/other","model":"later","permission_profile":{"type":"disabled"},"approval_policy":"never"}}
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	runtime, err := ReadRuntime("thread", path, "selected")
	if err != nil || runtime.Model != "actual" || runtime.Workspace != "/work" || runtime.Effort == nil || *runtime.Effort != "high" || len(runtime.PermissionProfile) == 0 {
		t.Fatalf("wrong runtime: %+v, %v", runtime, err)
	}
	for _, identity := range [][2]string{{"foreign", "selected"}, {"thread", "missing"}, {"thread", ""}} {
		if _, err := ReadRuntime(identity[0], path, identity[1]); err == nil {
			t.Fatalf("accepted unavailable identity %v", identity)
		}
	}
	if err := os.WriteFile(path, []byte(data+`{"type":"turn_context","payload":{"turn_id":"selected","cwd":"/work","model":"actual"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRuntime("thread", path, "selected"); err == nil {
		t.Fatal("incomplete latest evidence fell back to older authority")
	}
}

func TestAdmissionSettingsSurviveLongTurns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "root.jsonl")
	owned := `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_id":"root","thread_settings":{"model":"actual","model_provider_id":"selected","cwd":"/work","permission_profile":{"type":"managed","network":"restricted","file_system":{"type":"restricted","entries":[{"path":{"type":"path","path":"/denied"},"access":"deny"}]}},"approval_policy":"on-request","service_tier":"flex"}}}`
	padding := strings.Repeat("{\"type\":\"response_item\",\"payload\":{}}\n", (9<<20)/38)
	for _, suffix := range []string{"", strings.Replace(owned, `"actual"`, `"latest"`, 1) + "\n", strings.Replace(owned, `"actual"`, `"unfinished"`, 1)} {
		if err := os.WriteFile(path, []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"root\"}}\n"+owned+"\n"+padding+suffix), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := ReadAdmission("root", path)
		want := "actual"
		if strings.HasSuffix(suffix, "\n") {
			want = "latest"
		}
		if err != nil || got.Model != want || got.Provider != "selected" || got.Tier == nil || *got.Tier != "flex" || !bytes.Contains(got.PermissionProfile, []byte(`"access":"deny"`)) || string(got.ApprovalPolicy) != `"on-request"` {
			t.Fatalf("authority changed after long turn: %+v, %v", got, err)
		}
	}
}

func TestAdmissionRecordPages(t *testing.T) {
	for _, size := range []int{0, 1, (64 << 10) - 1, 64 << 10, (64 << 10) + 1, 3 * (64 << 10)} {
		for _, terminated := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%t", size, terminated), func(t *testing.T) {
				lines := []string{"header", strings.Repeat("x", size), "latest"}
				data := strings.Join(lines, "\n")
				if terminated {
					data += "\n"
				} else {
					lines = lines[:2]
				}
				slices.Reverse(lines)
				var got []string
				for line, err := range reverseRecordPages(strings.NewReader(data), int64(len(data)), 0, false) {
					if err != nil {
						t.Fatal(err)
					}
					got = append(got, string(line))
				}
				if !slices.Equal(got, lines) {
					t.Fatalf("record pages differ: lengths got %v, want %v", lengths(got), lengths(lines))
				}
			})
		}
	}
}

func lengths(lines []string) []int {
	result := make([]int, len(lines))
	for i, line := range lines {
		result[i] = len(line)
	}
	return result
}

func TestAdmissionRequiresThreadOwnedCanonicalSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "child.jsonl")
	valid := `{"type":"event_msg","payload":{"type":"thread_settings_applied","thread_id":"child","thread_settings":{"model":"actual","model_provider_id":"provider","cwd":"/child","permission_profile":{"type":"disabled"},"approval_policy":"never"}}}`
	for _, test := range []struct {
		name, record string
		valid        bool
	}{
		{"owned", valid, true},
		{"escaped event type", strings.Replace(valid, `thread_settings_applied`, `thread\u005fsettings_applied`, 1), true},
		{"inherited", strings.Replace(valid, `"thread_id":"child"`, `"thread_id":"parent"`, 1), false},
		{"unowned", strings.Replace(valid, `"thread_id":"child",`, "", 1), false},
		{"null authority", strings.Replace(valid, `{"type":"disabled"}`, "null", 1), false},
		{"invalid latest authority", valid + "\n" + strings.Replace(valid, `{"type":"disabled"}`, "null", 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"child\"}}\n"+test.record+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			snapshot, err := ReadAdmission("child", path)
			if (err == nil) != test.valid {
				t.Fatalf("admission valid=%t error=%v", test.valid, err)
			}
			if test.valid && (snapshot.Model != "actual" || string(snapshot.PermissionProfile) != `{"type":"disabled"}`) {
				t.Fatal("canonical settings changed")
			}
		})
	}
}

type admissionReadCounter struct {
	io.ReaderAt
	reads, largest int
}

func (r *admissionReadCounter) ReadAt(p []byte, off int64) (int, error) {
	r.reads++
	r.largest = max(r.largest, len(p))
	return r.ReaderAt.ReadAt(p, off)
}

func TestAdmissionRecordReadCost(t *testing.T) {
	data := strings.Repeat("old\n", 1<<20) + "latest\n"
	for _, lower := range []int64{0, 1 << 20} {
		reader := &admissionReadCounter{ReaderAt: strings.NewReader(data)}
		for record, err := range reverseRecordPages(reader, int64(len(data)), lower, lower != 0) {
			if err != nil || string(record) != "latest" {
				t.Fatalf("latest record: %q, %v", record, err)
			}
			break
		}
		if reader.reads != 1 || reader.largest != 64<<10 {
			t.Fatalf("latest record reread old history: %+v", reader)
		}
	}
}

func TestRecordWindowBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, data string
		lower      int64
		want       []string
	}{
		{"empty", "", 0, nil},
		{"full", "old\nnew\n", 0, []string{"new", "old"}},
		{"cut record", "old\nnew\n", 1, []string{"new"}},
		{"cut at delimiter", "old\nnew\n", 3, []string{"new"}},
		{"cut at record start", "old\nnew\n", 4, nil},
		{"unfinished final", "old\nnew", 0, []string{"old"}},
		{"all unfinished", "new", 0, nil},
		{"leading empty", "\nnew\n", 0, []string{"new"}},
		{"large cut record", strings.Repeat("x", 3*(64<<10)) + "\nnew\n", 1, []string{"new"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got []string
			for line, err := range reverseRecordPages(strings.NewReader(test.data), int64(len(test.data)), test.lower, true) {
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, string(line))
			}
			if !slices.Equal(got, test.want) {
				t.Fatalf("window records = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRecordPagesPartialEOFIsObservationalOnly(t *testing.T) {
	for _, partial := range []bool{false, true} {
		var got []string
		var failure error
		for line, err := range reverseRecordPages(strings.NewReader("old\nnew\n"), 13, 0, partial) {
			if err != nil {
				failure = err
				break
			}
			got = append(got, string(line))
		}
		if partial {
			if failure != nil || !slices.Equal(got, []string{"new", "old"}) {
				t.Fatalf("partial observation = %q, %v", got, failure)
			}
		} else if failure != io.EOF || len(got) != 0 {
			t.Fatalf("exact read accepted partial input: %q, %v", got, failure)
		}
	}
}

type truncatingRecordReader struct {
	*os.File
	cut   int64
	reads int
}

func (r *truncatingRecordReader) ReadAt(p []byte, off int64) (int, error) {
	r.reads++
	if r.reads == 2 {
		if err := r.Truncate(r.cut); err != nil {
			return 0, err
		}
	}
	return r.File.ReadAt(p, off)
}

func TestRecordPagesTruncationDiscardsNoncontiguousFragments(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "rollout-")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			prefix := "header\n" + `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":12`
			tail := "34}}}}\n"
			data := prefix + tail + strings.Repeat("x", (64<<10)-len(tail))
			if _, err := file.WriteString(data); err != nil {
				t.Fatal(err)
			}
			reader := &truncatingRecordReader{File: file, cut: int64(len(prefix) - 2)}
			var got []string
			var failure error
			for line, err := range reverseRecordPages(reader, int64(len(data)), 0, partial) {
				if err != nil {
					failure = err
					break
				}
				got = append(got, string(line))
			}
			if partial {
				if failure != nil || !slices.Equal(got, []string{"header"}) {
					t.Fatalf("truncated observation fabricated a record: %q, %v", got, failure)
				}
			} else if failure != io.EOF || len(got) != 0 {
				t.Fatalf("exact read accepted truncation: %q, %v", got, failure)
			}
		})
	}
}
