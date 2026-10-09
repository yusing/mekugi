package router

import (
	"bytes"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/tokenizer"
)

func guidanceTokenCount(t *testing.T, text string) int {
	t.Helper()
	codec, err := tokenizer.New()
	if err != nil {
		t.Fatal(err)
	}
	count, err := codec.Count(text)
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func TestJournalGuidanceHomeReads(t *testing.T) {
	t.Parallel()
	const workspace, home = "/workspace", "/user"
	for _, tc := range []struct {
		command string
		want    []string
	}{
		{`cat "$HOME/ONE.md"`, []string{home + "/ONE.md"}},
		{`mcat $HOME/ONE.md 1:2`, []string{home + "/ONE.md"}},
		{`inspect_file "${HOME}/ONE.md"`, []string{home + "/ONE.md"}},
		{`cat '${HOME}/ONE.md'`, []string{workspace + "/${HOME}/ONE.md"}},
		{`cat "$HOME/ONE.md" "$OTHER/TWO.md"`, nil},
		{`cat "${HOME:-$(touch sentinel)}/ONE.md"`, nil},
		{`cat "${HOME#prefix}/ONE.md"`, nil},
		{`cat "$HOME/$(printf ONE.md)"`, nil},
	} {
		if got := journalGuidanceReads(execCommandInput{Command: tc.command, Workdir: workspace}, home); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.command, got, tc.want)
		}
	}
	// Quoted HOME remains one path when the home directory contains spaces.
	for _, tc := range []struct {
		command string
		want    []string
	}{
		{`cat "$HOME/ONE.md"`, []string{"/user space/ONE.md"}},
		{`cat $HOME/ONE.md`, nil},
	} {
		if got := journalGuidanceReads(execCommandInput{Command: tc.command, Workdir: workspace}, "/user space"); !slices.Equal(got, tc.want) {
			t.Errorf("spaced HOME %s: got %v, want %v", tc.command, got, tc.want)
		}
	}
}

func TestJournalCompactionV2GuidancePriorityAndReadOrder(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	defer transform.Close()
	global := t.TempDir()
	t.Setenv("HOME", global)
	thread := transform.shellThreadID
	proxy.journalCompaction = "auto"
	if _, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "add", Kind: "context", Title: new("Ordered guidance")}}); err != nil {
		t.Fatal(err)
	}
	g1, g2 := filepath.Join(global, ".codex", "ONE.md"), filepath.Join(global, ".codex", "TWO.md")
	doc := filepath.Join(workspace, "doc", "DETAIL.md")
	for path, body := range map[string]string{
		g1: "Global one.\n", g2: "Global two.\n",
		filepath.Join(workspace, "ROOT1.md"): "Root one.\n",
		filepath.Join(workspace, "ROOT2.md"): "Root two.\n",
		filepath.Join(workspace, "NEW.md"):   "New context.\n",
		doc:                                  "# Detail\n" + strings.Repeat("PRIVATE-DOC-BODY\n", 8000) + "## Tail\nDetails.\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	agents := map[string]any{"role": "user", "content": "# AGENTS.md instructions for " + workspace + "\n### In ~/.codex\nRead ONE.md and TWO.md.\n--- project-doc ---\nRead ROOT1.md, ROOT2.md, NEW.md and doc/DETAIL.md."}
	read := func(id, command string) []any {
		return []any{
			map[string]any{"type": "function_call", "call_id": id, "name": nativeExecCommandToolName, "arguments": string(mustTestJSON(t, map[string]string{"cmd": command}))},
			map[string]any{"type": "function_call_output", "call_id": id, "output": "loaded"},
		}
	}
	history := append([]any{agents}, read("first-read", "cat ROOT2.md ROOT1.md "+shellQuoteArgument(g2)+" "+shellQuoteArgument(g1)+" doc/DETAIL.md")...)
	item, first := deliverV2ContinuityReset(t, proxy, workspace, thread, history...)
	want := []string{g2, g1, filepath.Join(workspace, "ROOT2.md"), filepath.Join(workspace, "ROOT1.md"), doc}
	check := func(g *journalGuidance, paths []string) {
		t.Helper()
		if g == nil || len(g.Commands) != len(paths) {
			t.Fatalf("guidance sources: %+v, want %v", g, paths)
		}
		for i, path := range paths {
			if !strings.HasSuffix(g.Commands[i], shellQuoteArgument(path)) {
				t.Fatalf("source order: %v, want %v", g.Commands, paths)
			}
		}
		if !strings.Contains(g.Text, "1-1 heading Detail") || !strings.Contains(g.Text, "8002-8002 heading Tail") || strings.Contains(g.Text, "PRIVATE-DOC-BODY") || !strings.HasPrefix(g.Commands[len(paths)-1], "inspect_file ") {
			t.Fatalf("nested document was not structural-only: %s", g.Text)
		}
	}
	check(first.Guidance, want)
	// Restoration reads durable order, not a live context or process handle.
	history = append([]any{agents, item}, read("later-read", "cat "+shellQuoteArgument(g1)+" "+shellQuoteArgument(g2)+" ROOT1.md ROOT2.md NEW.md")...)
	_, later := deliverV2ContinuityReset(t, proxy, workspace, thread, history...)
	want = append(want[:4:4], filepath.Join(workspace, "NEW.md"), doc)
	check(later.Guidance, want)
	// Replaying an older snapshot supplies candidates, not a new read order.
	// Without the initial order, actual last-context reads precede declarations.
	legacyText := strings.Replace(first.Guidance.Text, journalGuidanceHeader, "Guidance loaded before context reset, read again at reset.\n", 1)
	legacy, err := journalGuidanceItems(&journalGuidance{Tool: first.Guidance.Tool, Name: first.Guidance.Name, Commands: first.Guidance.Commands, Text: legacyText}, "resp_mekugi_compact_legacy")
	if err != nil {
		t.Fatal(err)
	}
	legacy = append(legacy,
		map[string]jsonv1.RawMessage{"type": mustTestJSON(t, "function_call"), "call_id": mustTestJSON(t, "last-read"), "name": mustTestJSON(t, nativeExecCommandToolName), "arguments": mustTestJSON(t, `{"cmd":"cat ROOT2.md"}`)},
		map[string]jsonv1.RawMessage{"type": mustTestJSON(t, "function_call_output"), "call_id": mustTestJSON(t, "last-read")})
	legacy = append(legacy, map[string]jsonv1.RawMessage{"role": mustTestJSON(t, "user"), "content": mustTestJSON(t, agents["content"])})
	g := collectJournalGuidance(t.Context(), legacy, "", workspace, nil, nil)
	check(g, []string{g1, g2, filepath.Join(workspace, "ROOT2.md"), filepath.Join(workspace, "ROOT1.md"), doc})
	// Ordering metadata cannot revive a source absent from the last context.
	old := filepath.Join(workspace, "OLD.md")
	if err := os.WriteFile(old, []byte("OLDER-CONTEXT-SENTINEL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g = collectJournalGuidance(t.Context(), legacy, "Read OLD.md.", workspace, append([]string{old}, first.Guidance.FirstReadOrder...), nil)
	check(g, []string{g2, g1, filepath.Join(workspace, "ROOT2.md"), filepath.Join(workspace, "ROOT1.md"), doc})
	if strings.Contains(g.Text, "OLDER-CONTEXT-SENTINEL") {
		t.Fatal("initial order revived an older-context source")
	}
}

func TestJournalGuidanceTokenBudget(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	path := filepath.Join(workspace, "GUIDE.md")
	// This whole source exceeds the old byte ceiling but is well below the token budget.
	body := strings.Repeat(" guidance", 9000) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	items := []map[string]jsonv1.RawMessage{
		{"type": mustTestJSON(t, "function_call"), "name": mustTestJSON(t, nativeExecCommandToolName), "call_id": mustTestJSON(t, "read"), "arguments": mustTestJSON(t, `{"cmd":"cat GUIDE.md"}`)},
		{"type": mustTestJSON(t, "function_call_output"), "call_id": mustTestJSON(t, "read")},
	}
	g := collectJournalGuidance(t.Context(), items, "Read GUIDE.md.", workspace, nil, nil)
	if g == nil || !strings.Contains(g.Text, body) || guidanceTokenCount(t, g.Text) > maxJournalGuidanceTokens {
		t.Fatal("token-fitting large source was not retained whole")
	}
	if _, err := journalGuidanceItems(g, "resp_mekugi_compact_test"); err != nil {
		t.Fatalf("large token-fitting snapshot was rejected on replay: %v", err)
	}
	// Dense text can exhaust tokens while using fewer bytes than the old ceiling.
	dense := strings.Repeat("一二三", 4500)
	if err := os.WriteFile(path, []byte(dense), 0o600); err != nil {
		t.Fatal(err)
	}
	if guidanceTokenCount(t, dense) <= maxJournalGuidanceTokens {
		t.Fatal("dense fixture does not exceed the token budget")
	}
	if got := collectJournalGuidance(t.Context(), items, "Read GUIDE.md.", workspace, nil, nil); got != nil {
		t.Fatal("token-overflowing source was retained")
	}
}

func TestJournalCompactionV2GuidanceFirstFiveResponses(t *testing.T) {
	t.Parallel()
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	defer transform.Close()
	proxy.journalCompaction = "auto"
	// A restored summary without guidance also does not consume a response.
	item, _ := deliverV2ContinuityReset(t, proxy, workspace, transform.shellThreadID)
	for _, path := range []string{"FIFTH.md", "PARALLEL.md", "LATE.md", "UNFINISHED.md"} {
		if err := os.WriteFile(filepath.Join(workspace, path), []byte(path+" body\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	history := []any{item, map[string]any{"role": "developer", "content": "Read FIFTH.md PARALLEL.md LATE.md UNFINISHED.md."}}
	for range 4 {
		history = append(history, map[string]any{"role": "assistant", "content": "Response without tools."}, map[string]any{"role": "user", "content": "Continue."})
	}
	call := func(id, command string) any {
		return map[string]any{"type": "function_call", "call_id": id, "name": nativeExecCommandToolName, "arguments": string(mustTestJSON(t, map[string]string{"cmd": command}))}
	}
	result := func(id string) any {
		return map[string]any{"type": "function_call_output", "call_id": id, "output": "read"}
	}
	history = append(history,
		map[string]any{"type": "reasoning", "summary": []any{}},
		map[string]any{"role": "assistant", "content": "Reading guidance."},
		call("fifth", "cat FIFTH.md"), call("parallel", "mcat PARALLEL.md"), call("unfinished", "cat UNFINISHED.md"),
		result("fifth"), result("parallel"),
		map[string]any{"type": "custom_tool_call", "call_id": "sixth", "name": "exec", "input": `text(await tools.exec_command({cmd:"cat LATE.md"}));`},
		map[string]any{"type": "custom_tool_call_output", "call_id": "sixth", "output": "read"})
	_, recovery := deliverV2ContinuityReset(t, proxy, workspace, transform.shellThreadID, history...)
	if recovery.Guidance == nil || !strings.Contains(recovery.Guidance.Text, "FIFTH.md body") || !strings.Contains(recovery.Guidance.Text, "PARALLEL.md body") ||
		strings.Contains(recovery.Guidance.Text, "LATE.md body") || strings.Contains(recovery.Guidance.Text, "UNFINISHED.md body") {
		t.Fatalf("wrong first-five guidance: %+v", recovery.Guidance)
	}
}

func TestJournalCompactionV2RetainsLoadedGuidance(t *testing.T) {
	bin := t.TempDir()
	trace := filepath.Join(bin, "reads")
	if err := os.WriteFile(filepath.Join(bin, "skills-mgr"), []byte("#!/bin/sh\nprintf called > "+shellQuoteArgument(trace)+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	defer transform.Close()
	thread := transform.shellThreadID
	t.Setenv("HOME", workspace)
	proxy.journalCompaction = "auto"
	proxy.duplicateOutput = true
	if _, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "add", Kind: "context", Title: new("Guidance reset")}}); err != nil {
		t.Fatal(err)
	}
	guide := filepath.Join(workspace, "GUIDE.md")
	if err := os.MkdirAll(filepath.Dir(guide), 0o755); err != nil {
		t.Fatal(err)
	}
	guideBody := "Guide v1.\nWhole guidance tail.\nScript failed\nWall time 0.1 seconds\nOutput:\nScript error:\nprinted guidance, not a host failure\nScript running with cell ID guidance\nWall time 0.1 seconds\nOutput:\nScript completed\nWall time 0.1 seconds\nOutput:\n"
	for path, content := range map[string]string{guide: guideBody, filepath.Join(workspace, "other.md"): "Unnamed body.\n", filepath.Join(workspace, "BIG.md"): strings.Repeat("x ", maxJournalGuidanceTokens)} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	agents := map[string]any{"role": "user", "content": "# AGENTS.md instructions for " + workspace + "\nRead GUIDE.md, BIG.md and MISSING.md."}
	cell := `text(await tools.exec_command({cmd: 'mcat -n 1 "$HOME/GUIDE.md" 1:1; cat other.md BIG.md MISSING.md; skills-mgr get bad; skills-mgr get demo 1:1'}));`
	item, recovery := deliverV2ContinuityReset(t, proxy, workspace, thread, agents,
		map[string]any{"type": "custom_tool_call", "status": "completed", "call_id": "call_read", "name": "exec", "input": cell},
		map[string]any{"type": "custom_tool_call_output", "call_id": "call_read", "output": []any{map[string]any{"type": "input_text", "text": "loaded"}}},
	)
	if recovery.Guidance == nil || !strings.HasSuffix(recovery.Text, journalGuidanceNotice) {
		t.Fatalf("reset retained no guidance: %q", recovery.Text)
	}
	text := recovery.Guidance.Text
	for _, want := range []string{"File: " + guide + "\nGuide v1.\nWhole guidance tail.\n", "Omitted by budget: cat " + filepath.Join(workspace, "BIG.md") + ": ", "Unavailable: cat " + filepath.Join(workspace, "MISSING.md") + ": "} {
		if !strings.Contains(text, want) {
			t.Fatalf("guidance missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Unnamed body.") || strings.Contains(text, "skills-mgr") {
		t.Fatalf("guidance retained an unnamed read or a skill:\n%s", text)
	}

	request, headers := v2ContinuityRequest(t, workspace, thread, "", "", item)
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
	var output bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), request, headers, "guidance-forward", provider, &output, nil, proxy); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 1 {
		t.Fatal("guidance turn was not forwarded")
	}
	var forwarded struct {
		Input []map[string]jsonv1.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(provider.forwarded[0], &forwarded); err != nil {
		t.Fatal(err)
	}
	if len(forwarded.Input) < 4 || jsonString(forwarded.Input[0], "role") != "assistant" || jsonString(forwarded.Input[3], "encrypted_content") != "provider-ciphertext" {
		t.Fatal("retained guidance did not follow the recovery message")
	}
	call, result := forwarded.Input[1], forwarded.Input[2]
	if jsonString(call, "type") != "custom_tool_call" || jsonString(call, "name") != "exec" || !strings.HasPrefix(jsonString(call, "call_id"), journalGuidanceCallID) ||
		jsonString(result, "type") != "custom_tool_call_output" || jsonString(result, "call_id") != jsonString(call, "call_id") {
		t.Fatalf("retained guidance call changed: %s %s", call, result)
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result["output"], &parts); err != nil || len(parts) != 1 || parts[0].Text != text {
		t.Fatal("retained guidance output changed")
	}
	if _, found, err := proxy.replayStore.lookup(t.Context(), workspace, jsonString(call, "call_id")); err != nil || found {
		t.Fatalf("synthetic guidance acquired execution history: found=%v err=%v", found, err)
	}
	proxy.activity.mu.Lock()
	for _, event := range proxy.activity.events {
		if event.callID == jsonString(call, "call_id") {
			t.Errorf("synthetic guidance emitted activity: %+v", event)
		}
	}
	proxy.activity.mu.Unlock()

	// A later reset recognizes the retained call and snapshots it again.
	if err := os.WriteFile(guide, []byte("Guide v2.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, later := deliverV2ContinuityReset(t, proxy, workspace, thread, agents, item, map[string]any{"role": "user", "content": "Continue after reset."})
	if later.Guidance == nil || !strings.Contains(later.Guidance.Text, "File: "+guide+"\nGuide v2.\n") {
		t.Fatalf("later reset did not carry guidance forward: %+v", later.Guidance)
	}
	// A skill-only history adds no snapshot or synthetic call.
	_, skills := deliverV2ContinuityReset(t, proxy, workspace, thread,
		map[string]any{"type": "function_call", "call_id": "skill-read", "name": nativeExecCommandToolName, "arguments": `{"cmd":"skills-mgr get demo"}`},
		map[string]any{"type": "function_call_output", "call_id": "skill-read", "output": "loaded"})
	if skills.Guidance != nil || strings.Contains(skills.Text, journalGuidanceNotice) {
		t.Fatal("skill-only history retained guidance")
	}
	if _, err := os.Stat(trace); !os.IsNotExist(err) {
		t.Fatalf("reset invoked the skill reader: %v", err)
	}
}

func TestJournalCompactionV2GuidanceNativeWholeSources(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	defer transform.Close()
	t.Setenv("HOME", workspace)
	thread := transform.shellThreadID
	proxy.journalCompaction = "auto"
	proxy.duplicateOutput = true
	if _, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "add", Kind: "context", Title: new("Whole sources")}}); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"GUIDE.md": "First line.\nRetain this tail.\nWall time: 0.1 seconds\nProcess running with session ID 7\nOutput:\nWall time: 0.1 seconds\nProcess exited with code 0\nOutput:\n" + strings.Repeat("guidance text\n", 20), "SKIP.md": "Skipped body.\n"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	agents := map[string]any{"role": "user", "content": "# AGENTS.md instructions for " + workspace + "\nRead ${HOME}/GUIDE.md and SKIP.md."}
	item, recovery := deliverV2ContinuityReset(t, proxy, workspace, thread, agents,
		map[string]any{"type": "function_call", "call_id": "call_native_read", "name": nativeExecCommandToolName, "arguments": string(mustTestJSON(t, map[string]string{"cmd": `mcat "${HOME}/GUIDE.md" 1:1; cat SKIP.md | head; x=$(cat SKIP.md); cat SKIP.md > /dev/null`, "workdir": workspace}))},
		map[string]any{"type": "function_call_output", "call_id": "call_native_read", "output": "loaded"},
		map[string]any{"type": "custom_tool_call", "call_id": "call_other", "name": "other", "input": `text(await tools.exec_command({cmd: "cat SKIP.md"}));`},
		map[string]any{"type": "custom_tool_call_output", "call_id": "call_other", "output": "loaded"})
	if recovery.Guidance == nil || !strings.Contains(recovery.Guidance.Text, "Retain this tail.") || strings.Contains(recovery.Guidance.Text, "Skipped body.") {
		t.Fatal("whole-source snapshots changed the literal-read boundary")
	}
	request, headers := v2ContinuityRequest(t, workspace, thread, "", "", item)
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
	var output bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), request, headers, "native-guidance-forward", provider, &output, nil, proxy); err != nil {
		t.Fatal(err)
	}
	var forwarded struct {
		Input []map[string]jsonv1.RawMessage `json:"input"`
	}
	if len(provider.forwarded) != 1 || json.Unmarshal(provider.forwarded[0], &forwarded) != nil || len(forwarded.Input) < 4 || jsonString(forwarded.Input[1], "type") != "function_call" || jsonString(forwarded.Input[1], "name") != nativeExecCommandToolName || jsonString(forwarded.Input[2], "output") != recovery.Guidance.Text {
		t.Fatal("native guidance call/result changed during forwarding")
	}
	if _, found, err := proxy.replayStore.lookup(t.Context(), workspace, jsonString(forwarded.Input[1], "call_id")); err != nil || found {
		t.Fatalf("synthetic native guidance acquired execution history: found=%v err=%v", found, err)
	}
}

func TestJournalCompactionV2GuidanceRejectsUnsafeSources(t *testing.T) {
	t.Parallel()
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	defer transform.Close()
	thread := transform.shellThreadID
	proxy.journalCompaction = "auto"
	if _, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "add", Kind: "context", Title: new("Safe guidance")}}); err != nil {
		t.Fatal(err)
	}
	guide := filepath.Join(workspace, "GUIDE.md")
	private := filepath.Join(t.TempDir(), "private.md")
	for path, content := range map[string]string{guide: "Safe guide.\n", private: "PRIVATE-SNAPSHOT-SENTINEL\n", filepath.Join(workspace, "SKILL.md"): "UNNAMED-SKILL-SENTINEL\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range map[string]string{"PRIVATE.md": private, "SYSTEM.md": "/proc/self/environ", "SAFE.md": guide, "outside": filepath.Dir(private)} {
		if err := os.Symlink(target, filepath.Join(workspace, name)); err != nil {
			t.Fatal(err)
		}
	}
	agents := map[string]any{"role": "user", "content": "# AGENTS.md instructions for " + workspace + "\nRead GUIDE.md, PRIVATE.md, SYSTEM.md, SAFE.md and outside/private.md."}
	item, recovery := deliverV2ContinuityReset(t, proxy, workspace, thread, agents,
		map[string]any{"type": "custom_tool_call", "call_id": "call_safe", "name": "exec", "input": `text(await tools.exec_command({cmd: "cat GUIDE.md PRIVATE.md SYSTEM.md SAFE.md outside/private.md SKILL.md"}));`},
		map[string]any{"type": "custom_tool_call_output", "call_id": "call_safe", "output": "loaded"})
	if recovery.Guidance == nil {
		t.Fatal("safe guide was not retained")
	}
	text := recovery.Guidance.Text
	if strings.Contains(text, "PRIVATE-SNAPSHOT-SENTINEL") || strings.Contains(text, "UNNAMED-SKILL-SENTINEL") || strings.Contains(text, "File: "+filepath.Join(workspace, "SYSTEM.md")) {
		t.Fatal("reset retained an unsafe source")
	}
	for _, want := range []string{"File: " + guide + "\nSafe guide.", "File: " + filepath.Join(workspace, "SAFE.md") + "\nSafe guide.", "resolved path is not instruction-named", "Unavailable:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("guidance missing %q", want)
		}
	}
	request, headers := v2ContinuityRequest(t, workspace, thread, "", "", item)
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
	var output bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), request, headers, "safe-guidance-forward", provider, &output, nil, proxy); err != nil {
		t.Fatal(err)
	}
	if len(provider.forwarded) != 1 || bytes.Contains(provider.forwarded[0], []byte("PRIVATE-SNAPSHOT-SENTINEL")) || bytes.Contains(provider.forwarded[0], []byte("UNNAMED-SKILL-SENTINEL")) {
		t.Fatal("unsafe guidance reached provider forwarding")
	}
}

func TestJournalCompactionV2GuidanceBoundsNotices(t *testing.T) {
	t.Parallel()
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	defer transform.Close()
	thread := transform.shellThreadID
	proxy.journalCompaction = "auto"
	if _, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "add", Kind: "context", Title: new("Bounded guidance")}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "GUIDE.md"), []byte("Guide body.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := []string{"GUIDE.md"}
	for i := range 900 {
		paths = append(paths, fmt.Sprintf("missing-%03d-%s.md", i, strings.Repeat("x", 120)))
	}
	agents := map[string]any{"role": "user", "content": "# AGENTS.md instructions for " + workspace + "\nRead " + strings.Join(paths, " ")}
	cell := "text(await tools.exec_command({cmd:" + string(mustTestJSON(t, "cat "+strings.Join(paths, " "))) + "}));"
	item, recovery := deliverV2ContinuityReset(t, proxy, workspace, thread, agents,
		map[string]any{"type": "custom_tool_call", "call_id": "call_many", "name": "exec", "input": cell},
		map[string]any{"type": "custom_tool_call_output", "call_id": "call_many", "output": "loaded"})
	request, headers := v2ContinuityRequest(t, workspace, thread, "", "", item)
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed","output":[]}`)}}}
	var output bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), request, headers, "bounded-guidance-forward", provider, &output, nil, proxy); err != nil {
		t.Fatalf("large source list made the reset unusable: %v", err)
	}
	if recovery.Guidance == nil || guidanceTokenCount(t, recovery.Guidance.Text) > maxJournalGuidanceTokens || !strings.Contains(recovery.Guidance.Text, "more sources") || len(provider.forwarded) != 1 {
		t.Fatal("guidance notices were not bounded and forwarded")
	}
	entries := strings.Split(strings.TrimSpace(recovery.Guidance.Text), "\n")
	last := strings.Fields(entries[len(entries)-1])
	if len(last) != 6 || last[1] != "more" || last[2] != "sources" {
		t.Fatal("truncated guidance notice joined the count to a source")
	}
}

func TestJournalCompactionV2GuidanceNativeSkillIdentity(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	defer transform.Close()
	proxy.journalCompaction = "auto"
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = list ]; then printf '<skills><skill name=\"shared\"/></skills>'; else [ \"$1:$2:$3\" = 'get:--codex:shared' ] || exit 1; printf 'Managed shared.\\n'; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "skills-mgr"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	only, a, b := filepath.Join(workspace, "native only.md"), filepath.Join(workspace, "shared-a.md"), filepath.Join(workspace, "shared-b.md")
	placeholder := filepath.Join(workspace, "managed", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(placeholder), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{only: "Native only v1.\n", a: "Native shared A.\n", b: "Native shared B.\n", placeholder: "Metadata is not instructions.\n", filepath.Join(filepath.Dir(placeholder), ".skills-mgr-placeholder"): "managed\n"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	draft := composerDraft{skills: []composerSkill{{name: "shared", path: a}, {name: "shared", path: b}, {name: "shared", path: placeholder}}}
	draft.snapshotSkillAttachments(workspace, os.Environ())
	selected := "<skill>\n<name>native-only</name>\n<path>" + only + "</path>\nNative only v1.\n</skill>"
	item, first := deliverV2ContinuityReset(t, proxy, workspace, transform.shellThreadID,
		map[string]any{"role": "user", "content": selected},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": draft.attachments[0]}}})
	check := func(recovery journalCompactionRecovery, nativeBody string) {
		t.Helper()
		if recovery.Guidance == nil || len(recovery.Guidance.Commands) != 4 {
			t.Fatalf("native/managed selection identity lost: %+v", recovery.Guidance)
		}
		for _, body := range []string{nativeBody, "Native shared A.", "Native shared B.", "Managed shared."} {
			if strings.Count(recovery.Guidance.Text, body) != 1 {
				t.Fatalf("wrong selected source for %q: %s", body, recovery.Guidance.Text)
			}
		}
	}
	check(first, "Native only v1.")
	if err := os.WriteFile(only, []byte("Native only v2.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	proxy = reopenV2ContinuityProxy(t, proxy)
	_, later := deliverV2ContinuityReset(t, proxy, workspace, transform.shellThreadID, item)
	check(later, "Native only v2.")
}
