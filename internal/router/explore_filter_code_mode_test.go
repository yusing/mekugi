package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const exploreCodeModeHeader = "Script completed\nWall time 0.25 seconds\nOutput:\n"

func exploreCodeModeSource(command, workdir string) string {
	return "text(await tools.exec_command({cmd:" + strconv.Quote(command) + ",workdir:" + strconv.Quote(workdir) + "}));"
}

func exploreCodeModeConsole(t *testing.T, header string, result any) string {
	t.Helper()
	return header + string(mustTestJSON(t, result))
}

func exploreCodeModeBlocks(header, result string, extra ...map[string]string) any {
	parts := []map[string]string{
		{"type": "input_text", "text": header},
		{"type": "input_text", "text": result},
	}
	return append(parts, extra...)
}

func exploreCodeModeRequest(t *testing.T, source, namespace string, output any) *parsedResponsesRequest {
	t.Helper()
	call := map[string]any{
		"type": "custom_tool_call", "name": "exec", "call_id": "code-call-1", "input": source,
	}
	if namespace != "" {
		call["namespace"] = namespace
	}
	input := []any{
		map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": "Fix live diff snapshot colors."}}},
		map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": "Looking for where live diff snapshots render."}}},
		call,
		map[string]any{"type": "custom_tool_call_output", "call_id": "code-call-1", "output": output},
	}
	return &parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustTestJSON(t, input)}}
}

func exploreCodeModeOutputRaw(t *testing.T, request *parsedResponsesRequest) json.RawMessage {
	t.Helper()
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	for _, item := range slices.Backward(items) {
		if jsonString(item, "type") == "custom_tool_call_output" {
			return bytes.Clone(item["output"])
		}
	}
	t.Fatal("Code Mode output not found")
	return nil
}

func exploreCodeModeParts(t *testing.T, request *parsedResponsesRequest) (header, result string) {
	t.Helper()
	var raw json.RawMessage
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	raw = items[len(items)-1]["output"]
	if json.Unmarshal(raw, &result) == nil {
		var ok bool
		header, result, ok = strings.Cut(result, "Output:\n")
		if !ok {
			t.Fatalf("Code Mode output has no Output header: %q", result)
		}
		return header + "Output:\n", result
	}
	var parts []map[string]string
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) != 2 {
		t.Fatalf("Code Mode output is neither a string nor two text blocks: %s", raw)
	}
	return parts[0]["text"], parts[1]["text"]
}

func exploreCodeModeResult(t *testing.T, exitCode any, stdout string) map[string]any {
	t.Helper()
	return map[string]any{
		"exit_code": exitCode,
		"output":    stdout,
		"stderr":    "preserve stderr metadata",
		"duration":  0.25,
		"host_meta": map[string]any{"invocation": 7, "unchanged": true},
	}
}

func TestExploreFilterCodeModeTransparentExec(t *testing.T) {
	dir, body := exploreFixture(t)
	for _, encoding := range []string{"string", "blocks", "metadata copy"} {
		t.Run(encoding, func(t *testing.T) {
			storeDir := t.TempDir()
			store, err := openMekugiReplayStore(storeDir)
			if err != nil {
				t.Fatal(err)
			}
			originalResult := exploreCodeModeResult(t, 0, body)
			if encoding == "metadata copy" {
				originalResult["retained"] = mustTestJSON(t, false)
			}
			resultJSON := string(mustTestJSON(t, originalResult))
			var wireOutput any = exploreCodeModeConsole(t, exploreCodeModeHeader, originalResult)
			if encoding == "blocks" {
				wireOutput = exploreCodeModeBlocks(exploreCodeModeHeader, resultJSON)
			}
			source := exploreCodeModeSource("rg -n snapshot", dir)
			if encoding == "metadata copy" {
				args := string(mustTestJSON(t, map[string]string{"cmd": "rg -n snapshot", "workdir": dir}))
				source = "const r=await tools.exec_command(" + args + "); text(JSON.stringify(Object.assign({},r,{retained:false})));"
			}
			request := exploreCodeModeRequest(t, source, "functions", wireOutput)
			judge := &fakeExploreJudge{score: func(exploreUnitState) float64 { return 0.01 }}
			filter := newExploreFilter(judge)
			filter.project(t.Context(), request, nil, dir, "", "/root", store)

			header, resultText := exploreCodeModeParts(t, request)
			if header != exploreCodeModeHeader {
				t.Fatalf("outer Code Mode header changed: %q", header)
			}
			var filtered map[string]json.RawMessage
			if err := json.Unmarshal([]byte(resultText), &filtered); err != nil {
				t.Fatalf("filtered result is not JSON: %v", err)
			}
			var stdout string
			if err := json.Unmarshal(filtered["output"], &stdout); err != nil {
				t.Fatalf("filtered stdout is not a JSON string: %v", err)
			}
			if stdout == body || len(stdout) >= len(body) {
				t.Fatalf("Code Mode stdout was not filtered: %d bytes", len(stdout))
			}
			if !strings.Contains(stdout, "[mekugi explore filter:") || !strings.Contains(stdout, "live/diff.go:1:") {
				t.Fatalf("filtered stdout lost its retained rows or recovery notice: %q", stdout)
			}
			for field := range originalResult {
				if field == "output" {
					continue
				}
				if !bytes.Equal(filtered[field], mustTestJSON(t, originalResult[field])) {
					t.Errorf("Code Mode metadata %q changed: got %s", field, filtered[field])
				}
			}
			if judge.calls.Load() == 0 {
				t.Fatal("eligible transparent exec result was not judged")
			}

			ref := regexp.MustCompile(`Full output: mread ([a-z0-9_]+)\]`).FindStringSubmatch(stdout)
			if ref == nil {
				t.Fatalf("filtered output has no mread reference: %s", stdout)
			}
			reopened, err := openMekugiReplayStore(storeDir)
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := reopened.readShellOutput(t.Context(), ref[1])
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Stdout != body {
				t.Fatalf("recovered stdout is not byte-exact: got %d bytes, want %d", len(recovered.Stdout), len(body))
			}

			calls := judge.calls.Load()
			replay := exploreCodeModeRequest(t, source, "functions", wireOutput)
			filter.project(t.Context(), replay, nil, dir, "", "/root", store)
			if judge.calls.Load() != calls || !bytes.Equal(exploreCodeModeOutputRaw(t, replay), exploreCodeModeOutputRaw(t, request)) {
				t.Fatal("same-filter replay was judged again or changed the projected result")
			}

			historical := exploreCodeModeRequest(t, source, "functions", wireOutput)
			var items []map[string]json.RawMessage
			if err := json.Unmarshal(historical.fields["input"], &items); err != nil {
				t.Fatal(err)
			}
			items = append(items, map[string]json.RawMessage{
				"type": mustTestJSON(t, "message"), "role": mustTestJSON(t, "assistant"),
				"content": mustTestJSON(t, []map[string]string{{"type": "output_text", "text": "done"}}),
			})
			historical.setInput(mustTestJSON(t, items))
			historicalBytes := exploreCodeModeOutputRaw(t, historical)
			restartedJudge := &fakeExploreJudge{score: func(exploreUnitState) float64 { return 0.01 }}
			newExploreFilter(restartedJudge).project(t.Context(), historical, nil, dir, "", "/root", reopened)
			if restartedJudge.calls.Load() != 0 || !bytes.Equal(exploreCodeModeOutputRaw(t, historical), historicalBytes) {
				t.Fatal("router-restart history was judged or changed")
			}
		})
	}
}

func TestExploreFilterCodeModeEligibilityBoundaries(t *testing.T) {
	dir, body := exploreFixture(t)
	result := exploreCodeModeResult(t, 0, body)
	resultJSON := string(mustTestJSON(t, result))
	completed := exploreCodeModeConsole(t, exploreCodeModeHeader, result)
	validSource := exploreCodeModeSource("rg -n snapshot", dir)
	failedHeader := "Script failed\nWall time 0.25 seconds\nOutput:\n"
	runningHeader := "Script running with cell ID cell-17\nWall time 0.25 seconds\nOutput:\n"
	missingExit := exploreCodeModeResult(t, nil, body)
	delete(missingExit, "exit_code")
	malformed := exploreCodeModeHeader + `{"output":"truncated`

	tests := []struct {
		name      string
		source    string
		namespace string
		output    any
		judgeErr  error
		wantCalls int32
	}{
		{"dynamic command", `await tools.exec_command({cmd: command})`, "functions", completed, nil, 0},
		{"modified result", `text((await tools.exec_command({cmd:"rg -n snapshot"})).output + " extra");`, "functions", completed, nil, 0},
		{"raw unstructured result", validSource, "functions", exploreCodeModeHeader + body, nil, 0},
		{"batch", `const r=await Promise.allSettled([tools.exec_command({cmd:"rg -n snapshot"}),tools.exec_command({cmd:"rg -n snapshot"})]); for(const row of r){text(row)}`, "functions", completed, nil, 0},
		{"multiple calls", validSource + ` text(await tools.exec_command({cmd:"rg -n snapshot"}));`, "functions", completed, nil, 0},
		{"shadowed text", `const text="shadow"; await tools.exec_command({cmd:"rg -n snapshot"});`, "functions", completed, nil, 0},
		{"failed cell", validSource, "functions", exploreCodeModeConsole(t, failedHeader, result), nil, 0},
		{"running cell", validSource, "functions", exploreCodeModeConsole(t, runningHeader, result), nil, 0},
		{"missing exit code", validSource, "functions", exploreCodeModeConsole(t, exploreCodeModeHeader, missingExit), nil, 0},
		{"null exit code", validSource, "functions", exploreCodeModeConsole(t, exploreCodeModeHeader, exploreCodeModeResult(t, nil, body)), nil, 0},
		{"nonzero search exit", validSource, "functions", exploreCodeModeConsole(t, exploreCodeModeHeader, exploreCodeModeResult(t, 1, body)), nil, 0},
		{"running process metadata", validSource, "functions", exploreCodeModeConsole(t, exploreCodeModeHeader, map[string]any{"exit_code": 0, "output": body, "session_id": 17}), nil, 0},
		{"truncated result JSON", validSource, "functions", malformed, nil, 0},
		{"malformed outer result", validSource, "functions", map[string]any{"output": body}, nil, 0},
		{"image block", validSource, "functions", exploreCodeModeBlocks(exploreCodeModeHeader, resultJSON, map[string]string{"type": "image", "text": "not text"}), nil, 0},
		{"extra text block", validSource, "functions", exploreCodeModeBlocks(exploreCodeModeHeader, resultJSON, map[string]string{"type": "input_text", "text": "extra"}), nil, 0},
		{"namespace mismatch", validSource, "other", completed, nil, 0},
		{"judge failure", validSource, "functions", completed, errors.New("unavailable"), 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := exploreCodeModeRequest(t, test.source, test.namespace, test.output)
			before := exploreCodeModeOutputRaw(t, request)
			judge := &fakeExploreJudge{err: test.judgeErr, score: func(exploreUnitState) float64 { return 0.01 }}
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			newExploreFilter(judge).project(t.Context(), request, nil, dir, "", "/root", store)
			if !bytes.Equal(exploreCodeModeOutputRaw(t, request), before) {
				t.Fatal("ineligible, incomplete, or failed result changed")
			}
			if got := judge.calls.Load(); got != test.wantCalls {
				t.Fatalf("judge calls = %d, want %d", got, test.wantCalls)
			}
		})
	}
}

func TestExploreFilterCodeModeAllowsDiagnosticExitOne(t *testing.T) {
	dir, body := exploreFixture(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	command := "go vet ./..."
	result := exploreCodeModeResult(t, 1, body)
	request := exploreCodeModeRequest(t, exploreCodeModeSource(command, dir), "functions",
		exploreCodeModeConsole(t, exploreCodeModeHeader, result))
	judge := &fakeExploreJudge{score: func(exploreUnitState) float64 { return 0.01 }}
	newExploreFilter(judge).project(t.Context(), request, nil, dir, "", "/root", store)
	_, filteredResult := exploreCodeModeParts(t, request)
	var output map[string]json.RawMessage
	if err := json.Unmarshal([]byte(filteredResult), &output); err != nil {
		t.Fatal(err)
	}
	var stdout string
	if err := json.Unmarshal(output["output"], &stdout); err != nil {
		t.Fatal(err)
	}
	if judge.calls.Load() == 0 || stdout == body || !strings.Contains(stdout, "[mekugi explore filter:") {
		t.Fatalf("diagnostics exit 1 was not filtered: calls=%d stdout=%q", judge.calls.Load(), stdout)
	}
}

func TestExploreFilterCodeModePrintedStdout(t *testing.T) {
	dir, body := exploreFixture(t)
	args := string(mustTestJSON(t, map[string]string{"cmd": "rg -n snapshot", "workdir": dir}))
	for name, source := range map[string]string{
		"binding":   "const r=await tools.exec_command(" + args + "); text(r.output);",
		"awaited":   "text((await tools.exec_command(" + args + ")).output)",
		"commented": "// search\nlet result = await tools.exec_command(" + args + ");\ntext(result.output)",
	} {
		for _, encoding := range []string{"string", "blocks"} {
			t.Run(name+"/"+encoding, func(t *testing.T) {
				store, err := openMekugiReplayStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				var output any = exploreCodeModeHeader + body
				if encoding == "blocks" {
					output = exploreCodeModeBlocks(exploreCodeModeHeader, body)
				}
				request := exploreCodeModeRequest(t, source, "", output)
				judge := &fakeExploreJudge{score: func(unit exploreUnitState) float64 {
					if strings.HasPrefix(unit.Path, "live/") {
						return 0.9
					}
					return 0.01
				}}
				newExploreFilter(judge).project(t.Context(), request, nil, dir, "", "/root", store)
				header, stdout := exploreCodeModeParts(t, request)
				if header != exploreCodeModeHeader {
					t.Fatalf("outer Code Mode header changed: %q", header)
				}
				ref := regexp.MustCompile(`\[mekugi explore filter: omitted 3 of 6 files .*Full output: mread ([a-z0-9_]+)\]\n$`).FindStringSubmatch(stdout)
				if ref == nil || !strings.HasPrefix(stdout, "live/diff.go:1:") || strings.Contains(stdout, "catalog/d.go:") {
					t.Fatalf("printed stdout was not filtered: %q", stdout)
				}
				recovered, err := store.readShellOutput(t.Context(), ref[1])
				if err != nil || recovered.Stdout != body {
					t.Fatalf("recovered stdout differs: %v", err)
				}
			})
		}
	}
}

func TestToolActivityUnwrapExecOutputRejectsOtherPrints(t *testing.T) {
	for _, source := range []string{
		`text(await tools.exec_command({cmd:"rg x"}))`,
		`const r=await tools.exec_command({cmd:"rg x"}); text(r.output + "!");`,
		`const r=await tools.exec_command({cmd:"rg x"}); text(r.stderr);`,
		`const r=await tools.exec_command({cmd:"rg x"}); text(r.output); text(r.output);`,
		`const text=await tools.exec_command({cmd:"rg x"}); text(text.output);`,
		`const r=await tools.exec_command({cmd:command}); text(r.output);`,
		`const r=await Promise.all([tools.exec_command({cmd:"rg x"})]); text(r.output);`,
		`text((await tools.exec_command({cmd:"rg x"}))?.output)`,
	} {
		if _, ok := toolActivityUnwrapExecOutput(source); ok {
			t.Errorf("accepted %q", source)
		}
	}
}
