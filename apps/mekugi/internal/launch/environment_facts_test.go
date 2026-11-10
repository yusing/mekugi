package launch

import (
	"bytes"
	"context"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"html"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi/testsupport/moduletest"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func TestFrozenInitialPrefixProviderLifecycle(t *testing.T) {
	workspace := gitTestWorkspace(t)
	newProxy := func() *observationFixture {
		registry, err := buildToolRegistryForTest(t, t.Context())
		if err != nil {
			t.Fatal(err)
		}
		guidance := *registry.Guidance
		registry.Guidance = &guidance
		t.Cleanup(func() { _ = registry.Close() })
		return newProxyWithSharedTestRegistry(t, registry)
	}
	proxy := newProxy()
	acquisitions := 0
	create := proxy.registry.Guidance.InitialContext
	proxy.registry.Guidance.InitialContext = func(ctx context.Context, cwd string) ([]jsonv1.RawMessage, error) {
		items, err := create(ctx, cwd)
		acquisitions++
		return append([]jsonv1.RawMessage{responses.MustMarshalJSON(map[string]string{"role": "developer", "content": "non-environment acquisition " + strconv.Itoa(acquisitions)})}, items...), err
	}
	// A later phase is deliberately stateful. The final owner, rather than an
	// environment-only cache or this individual feature, enforces immutability.
	version := "first feature version"
	late := proxy.registry.Guidance.AfterAttachments
	proxy.registry.Guidance.AfterAttachments = func(input binding.InstructionInput, skills bool) (binding.InstructionUpdate, error) {
		update, err := late(input, skills)
		if err != nil {
			return update, err
		}
		var items []jsontext.Value
		if err := json.Unmarshal(update.Input, &items); err != nil {
			return update, err
		}
		update.Input = responses.MustMarshalJSON(append([]jsontext.Value{responses.MustMarshalJSON(map[string]string{"role": "developer", "content": version})}, items...))
		update.InputChanged = true
		return update, nil
	}
	forward := func(p *observationFixture, thread, window, authority, task string) []byte {
		t.Helper()
		headers := serverMetadataHeaders(t, "turn", map[string]jsonv1.RawMessage{workspace: nil})
		headers.Set(codexresponses.ThreadIDHeader, thread)
		metadata, _ := codexresponses.DecodeTurnMetadata(headers)
		metadata.ContextWindowID = window
		headers.Set(codexresponses.TurnMetadataHeader, string(responses.MustMarshalJSON(metadata)))
		request := serverRequest(t, func(fields map[string]any) {
			items := []any{map[string]string{"role": "developer", "content": authority}, testFlatCodeModeAdditionalTools(testCodeModeDescription), map[string]string{"role": "user", "content": "first task"}}
			if task != "first task" {
				items = append(items, map[string]string{"role": "user", "content": task})
			}
			fields["input"] = items
		})
		provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed"}`)}}}
		var output bytes.Buffer
		if err := executeRequest(t.Context(), t.Context(), request, headers, t.Name(), provider, &output, nil, p); err != nil {
			t.Fatal(err)
		}
		return provider.forwarded[0]
	}
	first := forward(proxy, "thread-1", "original", "parent permissions", "first task")
	version = "later feature version"
	next := forward(proxy, "thread-1", "original", "parent permissions", "new user input")
	if acquisitions != 1 || !bytes.Contains(next, []byte("first feature version")) || bytes.Contains(next, []byte(version)) || !bytes.Contains(next, []byte("new user input")) {
		t.Fatal("later feature rewrote the frozen initial prefix or new input was lost")
	}
	firstRequest, err := codexresponses.ParseRequest(first)
	if err != nil {
		t.Fatal(err)
	}
	var sent []jsontext.Value
	if err := json.Unmarshal(firstRequest.Fields["input"], &sent); err != nil {
		t.Fatal(err)
	}
	nextRequest, err := codexresponses.ParseRequest(next)
	if err != nil {
		t.Fatal(err)
	}
	exchange := confirmedHistoryExchange(t, sent, false)
	nextRequest.Fields["previous_response_id"] = responses.MustMarshalJSON("parent")
	if reason, err := exchange.Reconcile(&nextRequest, true); err != nil || reason != "matching_prefix" || nextRequest.CachedInput != len(sent) {
		t.Fatalf("frozen provider prefix did not remain cache eligible: %s, %v", reason, err)
	}
	nextRequest.Fields["input"] = bytes.Replace(nextRequest.Fields["input"], []byte("first task"), []byte("changed history"), 1)
	if reason, err := exchange.Reconcile(&nextRequest, true); err != nil || reason != "changed_prefix" || !nextRequest.RebaseInput {
		t.Fatal("initial-prefix freezing weakened conversation-history confirmation")
	}
	// Independent child runtimes do not share Observations. Their admitted import
	// carries a frozen prefix value, while the native child supplies its authority.
	inherited := proxy.control.CaptureInputPrefix("thread-1", workspace)
	proxy.control.RefreshContext("thread-1")
	refreshed := forward(proxy, "thread-1", "refreshed", "parent permissions", "fresh task")
	if acquisitions != 2 || !bytes.Contains(refreshed, []byte(version)) {
		t.Fatal("fresh context did not acquire current contributions")
	}
	child := newProxy()
	child.registry.Guidance.InitialContext = func(context.Context, string) ([]jsonv1.RawMessage, error) {
		t.Fatal("same-workspace full-history child reacquired frozen contributions")
		return nil, nil
	}
	child.control.AdmitInputPrefix("child", workspace, inherited)
	childWire := forward(child, "child", "child-window", "child role and restricted permissions", "child assignment")
	if environmentFacts(t, childWire) != environmentFacts(t, first) || !bytes.Contains(childWire, []byte("non-environment acquisition 1")) || !bytes.Contains(childWire, []byte("child role and restricted permissions")) || bytes.Contains(childWire, []byte("parent permissions")) {
		t.Fatal("child lost immutable contributions or inherited parent authority")
	}
	if proxy.control.CaptureInputPrefix("thread-1", t.TempDir()) != nil {
		t.Fatal("different workspace inherited the parent's context")
	}
	proxy.control.RewindContext("thread-1", workspace)
	rewound := forward(proxy, "thread-1", "rewound-window", "parent permissions", "rewound task")
	if acquisitions != 2 || environmentFacts(t, rewound) != environmentFacts(t, refreshed) {
		t.Fatal("rewind changed the applicable initial context")
	}
}

func environmentFacts(t *testing.T, raw []byte) string {
	t.Helper()
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	instructions := responses.StringField(fields, "instructions")
	if strings.Contains(instructions, "<mekugi_environment_facts>") {
		t.Fatal("mutable environment facts invalidated the static base instructions")
	}
	var items []map[string]jsontext.Value
	if err := json.Unmarshal(fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	boundary, supplement := len(items), -1
	for index, item := range items {
		if responses.StringField(item, "role") == "user" && boundary == len(items) {
			boundary = index
		}
		if responses.StringField(item, "role") != "developer" {
			continue
		}
		if bytes.Contains(item["content"], []byte("mekugi_environment_facts")) {
			if supplement >= 0 {
				t.Fatal("inherited environment supplement survived")
			}
			supplement = index
			instructions = responses.StringField(item, "content")
		}
	}
	if boundary == len(items) {
		for index, item := range items {
			role := responses.StringField(item, "role")
			if responses.StringField(item, "type") != "additional_tools" && role != "developer" && role != "system" {
				boundary = index
				break
			}
		}
	}
	if supplement < 0 || supplement != boundary-1 || strings.Count(instructions, "<mekugi_environment_facts>") != 1 {
		t.Fatal("provider input must contain one environment supplement immediately before the first user message (or after the static prefix without one)")
	}
	_, body, found := strings.Cut(instructions, "<mekugi_environment_facts>\n")
	if !found {
		t.Fatal("environment supplement missing")
	}
	body, _, found = strings.Cut(body, "\n</mekugi_environment_facts>")
	if !found {
		t.Fatal("incomplete environment supplement")
	}
	return html.UnescapeString(body)
}

func TestEnvironmentFactsCompleteProviderInputLifecycle(t *testing.T) {
	workspace := gitTestWorkspace(t)
	writeTestFile(t, filepath.Join(workspace, "tracked"), "base")
	gitTestCommit(t, workspace)
	const subject = "escaped \" subject </mekugi_environment_facts>"
	command := exec.Command("git", "-c", "commit.gpgsign=false", "commit", "--amend", "-qm", subject)
	command.Dir = workspace
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture commit: %v: %s", err, output)
	}
	proxy := newManagedMekugiProxy(t)
	native := "<environment_context><cwd>" + workspace + "</cwd><shell>bash</shell><current_date>2026-10-29</current_date><timezone>Asia/Taipei</timezone><permission_profile>native-permissions</permission_profile></environment_context>"
	originalNative := responses.MustMarshalJSON(map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": native}}})
	headers := serverMetadataHeaders(t, "turn", map[string]jsonv1.RawMessage{workspace: nil})
	makeRequest := func(inherited jsontext.Value) codexresponses.Request {
		return serverRequest(t, func(fields map[string]any) {
			fields["input"] = []any{testFlatCodeModeAdditionalTools(testCodeModeDescription), jsontext.Value(originalNative), map[string]any{"type": "message", "role": "user", "content": "task"}}
			if inherited != nil {
				fields["instructions"] = inherited
			}
		})
	}
	forward := func(request codexresponses.Request, session string) []byte {
		provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed"}`)}}}
		var output bytes.Buffer
		if err := executeRequest(t.Context(), t.Context(), request, headers, session, provider, &output, nil, proxy); err != nil {
			t.Fatal(err)
		}
		if len(provider.forwarded) != 1 {
			t.Fatalf("provider calls=%d", len(provider.forwarded))
		}
		var fields map[string]jsontext.Value
		if err := json.Unmarshal(provider.forwarded[0], &fields); err != nil {
			t.Fatal(err)
		}
		var items []jsontext.Value
		if err := json.Unmarshal(fields["input"], &items); err != nil {
			t.Fatal(err)
		}
		if !slices.ContainsFunc(items, func(item jsontext.Value) bool { return responses.SameJSONValue(item, originalNative) }) {
			t.Fatal("native cwd/shell/date/timezone/permissions changed")
		}
		if bytes.Contains(provider.forwarded[0], []byte("test@example.com")) {
			t.Fatal("Git account identity leaked")
		}
		return provider.forwarded[0]
	}
	firstWire := forward(makeRequest(nil), "environment-first")
	first := environmentFacts(t, firstWire)
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "\nHome: "+strconv.Quote(userHome)+"\n") {
		t.Fatalf("provider input omitted user home: %s", first)
	}
	if !strings.Contains(first, "Recent commits:\n") || !strings.Contains(first, "\nOS: ") || !strings.Contains(first, "\nPlatform: ") || strings.Contains(first, workspace) {
		t.Fatalf("facts missing: %+v", first)
	}
	if !strings.Contains(first, " "+subject+"\n") && !strings.HasSuffix(first, " "+subject) {
		t.Fatal("provider projection changed escaped commit subject")
	}
	writeTestFile(t, filepath.Join(workspace, "new\n\"file"), "fresh")
	repeated := environmentFacts(t, forward(makeRequest(nil), "environment-first"))
	if repeated != first || !strings.Contains(repeated, "Status:\nclean\n") {
		t.Fatal("ordinary request rescanned")
	}
	// This is the shared authoritative lifecycle signal, not a prompt-text trigger.
	proxy.control.RefreshContext("thread-1")
	refreshedWire := forward(makeRequest(nil), "environment-first")
	refreshed := environmentFacts(t, refreshedWire)
	if !strings.Contains(refreshed, "\n?? ") || refreshed == first {
		t.Fatal("fresh context did not refresh")
	}
	if !strings.Contains(refreshed, `\n`) {
		t.Fatal("provider projection lost escaped filename")
	}
	resumed := environmentFacts(t, forward(makeRequest(nil), "environment-resumed"))
	if resumed == refreshed {
		t.Fatal("resumed runtime reused prior snapshot")
	}
	changedWorkspace := t.TempDir()
	headers = serverMetadataHeaders(t, "turn", map[string]jsonv1.RawMessage{changedWorkspace: nil})
	changed := environmentFacts(t, forward(makeRequest(nil), "environment-resumed"))
	if !strings.Contains(changed, "\nGit repository: no") || changed == resumed || strings.Contains(changed, "Recent commits:") || strings.Contains(changed, "repository unavailable") {
		t.Fatal("authoritative same-thread workspace change reused facts")
	}
	// A full-history child can contain a prior projection. Only its own supplement survives.
	var parentFields map[string]jsontext.Value
	if err := json.Unmarshal(refreshedWire, &parentFields); err != nil {
		t.Fatal(err)
	}
	childWorkspace := t.TempDir()
	headers = serverMetadataHeaders(t, "turn", map[string]jsonv1.RawMessage{childWorkspace: nil})
	headers.Set(codexresponses.ThreadIDHeader, "environment-child")
	child := environmentFacts(t, forward(makeRequest(parentFields["instructions"]), "environment-child-session"))
	if !strings.Contains(child, "\nGit repository: no") || strings.Contains(child, "Recent commits:") || strings.Contains(child, "repository unavailable") {
		t.Fatal("different-workspace child inherited Git facts")
	}
}

func TestEnvironmentFactsPrewarmDoesNotAcquire(t *testing.T) {
	workspace := gitTestWorkspace(t)
	proxy := newManagedMekugiProxy(t)
	request := serverRequest(t, nil)
	request.Fields["generate"] = []byte("false")
	headers := serverMetadataHeaders(t, "prewarm", map[string]jsonv1.RawMessage{workspace: nil})
	metadata, valid := codexresponses.DecodeTurnMetadata(headers)
	if _, err := proxy.Prepare(t.Context(), &request, requestexecution.Preparation{Session: "environment-prewarm", Thread: "thread-1", Metadata: metadata, MetadataValid: valid, Prewarm: true}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(responses.MustMarshalJSON(request.Fields), []byte("mekugi_environment_facts")) {
		t.Fatal("prewarm acquired environment facts")
	}
	// No prewarm acquisition may freeze the unborn state before inference.
	writeTestFile(t, filepath.Join(workspace, "tracked"), "base")
	gitTestCommit(t, workspace)
	request = serverRequest(t, nil)
	headers = serverMetadataHeaders(t, "turn", map[string]jsonv1.RawMessage{workspace: nil})
	provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed"}`)}}}
	var output bytes.Buffer
	if err := executeRequest(t.Context(), t.Context(), request, headers, "environment-prewarm", provider, &output, nil, proxy); err != nil {
		t.Fatal(err)
	}
	if facts := environmentFacts(t, provider.forwarded[0]); strings.Contains(facts, "\nHEAD: unborn\n") {
		t.Fatal("prewarm cached stale facts")
	}
}

func TestEnvironmentFactsOrderedProviderPrefix(t *testing.T) {
	const inherited = "<mekugi_environment_facts>\nparent facts\n</mekugi_environment_facts>"
	for _, multipart := range []bool{false, true} {
		t.Run(strconv.FormatBool(multipart), func(t *testing.T) {
			workspace := t.TempDir()
			proxy := newManagedMekugiProxy(t)
			headers := serverMetadataHeaders(t, "turn", map[string]jsonv1.RawMessage{workspace: nil})
			const staticText = "Static permissions and role.\n"
			var staticContent any = staticText + "\ntrailing policy"
			var inheritedStaticContent any = staticText + inherited + "\ntrailing policy"
			if multipart {
				staticContent = []map[string]string{{"type": "input_text", "text": "Static permissions and role.\n"}, {"type": "input_text", "text": ""}, {"type": "input_image", "image_url": "data:image/png;base64,AA=="}}
				inheritedStaticContent = []map[string]string{{"type": "input_text", "text": "Static permissions and role.\n"}, {"type": "input_text", "text": inherited}, {"type": "input_image", "image_url": "data:image/png;base64,AA=="}}
			}
			static := responses.MustMarshalJSON(map[string]any{"type": "message", "role": "developer", "id": "static", "content": staticContent})
			inheritedStatic := responses.MustMarshalJSON(map[string]any{"type": "message", "role": "developer", "id": "static", "content": inheritedStaticContent})
			history := []jsontext.Value{
				responses.MustMarshalJSON(map[string]string{"role": "user", "content": "task"}),
				responses.MustMarshalJSON(map[string]string{"type": "function_call", "call_id": "call", "name": "lookup", "arguments": "{}"}),
				responses.MustMarshalJSON(map[string]string{"type": "function_call_output", "call_id": "call", "output": inherited}),
				responses.MustMarshalJSON(map[string]string{"role": "assistant", "content": "answer"}),
			}
			var content any = inherited
			if multipart {
				content = []map[string]string{{"type": "input_text", "text": inherited}}
			}
			request := serverRequest(t, func(fields map[string]any) {
				fields["instructions"] = "Static base.\n\n" + inherited + "\n\nStatic suffix."
				fields["tools"] = []any{}
				fields["input"] = append([]jsontext.Value{responses.MustMarshalJSON(map[string]any{"role": "developer", "content": content}), inheritedStatic}, history...)
			})
			forward := func(request codexresponses.Request) codexresponses.Request {
				request.Fields = maps.Clone(request.Fields)
				provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed"}`)}}}
				var output bytes.Buffer
				if err := executeRequest(t.Context(), t.Context(), request, headers, "ordered-facts", provider, &output, nil, proxy); err != nil {
					t.Fatal(err)
				}
				environmentFacts(t, provider.forwarded[0])
				got, err := codexresponses.ParseRequest(provider.forwarded[0])
				if err != nil {
					t.Fatal(err)
				}
				if responses.StringField(got.Fields, "instructions") != "Static base.\n\nStatic suffix." {
					t.Fatal("legacy environment cleanup changed static base")
				}
				var items []jsontext.Value
				if err := json.Unmarshal(got.Fields["input"], &items); err != nil {
					t.Fatal(err)
				}
				if len(items) != len(history)+2 || !responses.SameJSONValue(items[0], static) {
					t.Fatal("static developer prefix changed or inherited context survived")
				}
				for index, original := range history {
					if !responses.SameJSONValue(items[index+2], original) {
						t.Fatalf("projection changed history item %d: got %s, want %s", index, items[index+2], original)
					}
				}
				return got
			}
			first := forward(request)
			repeated := forward(first)
			if !bytes.Equal(first.Fields["input"], repeated.Fields["input"]) {
				t.Fatal("repeated provider preparation was not idempotent")
			}
			proxy.control.RefreshContext("thread-1")
			refreshed := forward(first)
			if !bytes.Equal(first.Fields["instructions"], refreshed.Fields["instructions"]) || bytes.Equal(first.Fields["input"], refreshed.Fields["input"]) {
				t.Fatal("refresh did not isolate mutable facts from the static prefix")
			}
		})
	}
}

func TestEnvironmentFactsSideForkProviderInput(t *testing.T) {
	workspace := gitTestWorkspace(t)
	proxy := newManagedMekugiProxy(t)
	forward := func(thread string) string {
		headers := serverMetadataHeaders(t, "turn", map[string]jsonv1.RawMessage{workspace: nil})
		headers.Set(codexresponses.ThreadIDHeader, thread)
		provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed"}`)}}}
		var output bytes.Buffer
		if err := executeRequest(t.Context(), t.Context(), serverRequest(t, nil), headers, "side-facts", provider, &output, nil, proxy); err != nil {
			t.Fatal(err)
		}
		return environmentFacts(t, provider.forwarded[0])
	}
	first := forward("thread-1")
	writeTestFile(t, filepath.Join(workspace, "after-fork"), "new")
	proxy.preparer().AdmitFork("thread-1", "side", workspace)
	if forward("side") != first {
		t.Fatal("confirmed same-workspace side fork lost its original snapshot")
	}
	proxy.control.RefreshContext("side")
	if refreshed := forward("side"); refreshed == first || !strings.Contains(refreshed, "after-fork") {
		t.Fatal("side reset did not refresh independently")
	}
	if forward("thread-1") != first {
		t.Fatal("side reset changed Main's snapshot")
	}
}

func TestEnvironmentFactsProviderInputForms(t *testing.T) {
	for _, input := range []any{"task", nil, []any{}} {
		t.Run(string(responses.MustMarshalJSON(input)), func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			request := serverRequest(t, func(fields map[string]any) {
				fields["input"], fields["tools"] = input, []any{}
			})
			provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed"}`)}}}
			var output bytes.Buffer
			if err := executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", nil), t.Name(), provider, &output, nil, proxy); err != nil {
				t.Fatal(err)
			}
			environmentFacts(t, provider.forwarded[0])
			var got struct {
				Input []struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"input"`
			}
			if err := json.Unmarshal(provider.forwarded[0], &got); err != nil {
				t.Fatal(err)
			}
			if input == "task" {
				if len(got.Input) != 2 || got.Input[1].Role != "user" || got.Input[1].Content != "task" {
					t.Fatal("string input lost its user message")
				}
			} else if len(got.Input) != 1 {
				t.Fatal("empty input gained a fabricated user message")
			}
		})
	}
}

func TestEnvironmentFactsMalformedProviderInput(t *testing.T) {
	for _, text := range []string{"<mekugi_environment_facts>", "</mekugi_environment_facts>", "</mekugi_environment_facts><mekugi_environment_facts>", "<mekugi_environment_facts><mekugi_environment_facts></mekugi_environment_facts>"} {
		for _, developer := range []bool{false, true} {
			proxy := newManagedMekugiProxy(t)
			request := serverRequest(t, func(fields map[string]any) {
				if developer {
					fields["input"] = append([]any{map[string]any{"role": "developer", "content": []map[string]string{{"type": "input_text", "text": text}}}}, fields["input"].([]any)...)
				} else {
					fields["instructions"] = text
				}
			})
			provider := new(serverFakeProvider)
			var output bytes.Buffer
			if err := executeRequest(t.Context(), t.Context(), request, serverMetadataHeaders(t, "turn", nil), t.Name(), provider, &output, nil, proxy); err == nil || len(provider.forwarded) != 0 {
				t.Fatal("malformed environment markers reached the provider")
			}
		}
	}
}

func TestEnvironmentCapabilityShellOverride(t *testing.T) {
	forwarded := []string{"exec", "-c", `shell_environment_policy.set.UNRELATED="kept"`, "-c", `shell_environment_policy.set.MEKUGI_ENVIRONMENT_FACTS="1"`, "--", "prompt"}
	args := codexArgs("http://localhost/v1", forwarded, false, true, nil)
	end := slices.Index(args, "--")
	original := slices.Index(forwarded, "--")
	if !slices.Equal(args[:original], forwarded[:original]) || !slices.Equal(args[end:], forwarded[original:]) {
		t.Fatal("caller shell policy or prompt changed")
	}
	overrides := args[original:end]
	if !slices.Contains(overrides, `shell_environment_policy.set.MEKUGI_ENVIRONMENT_FACTS=""`) {
		t.Fatal("tool capability was not cleared after caller overrides")
	}
}

func TestEnvironmentCapabilityPerInvocation(t *testing.T) {
	inherited := []string{"MEKUGI_ENVIRONMENT_FACTS=1", "UNRELATED=kept", "MEKUGI_ENVIRONMENT_FACTS=other"}
	for _, installed := range []bool{false, true} {
		got := codexEnvironmentCapability(inherited, installed)
		want := []string{"UNRELATED=kept"}
		if installed {
			want = append(want, "MEKUGI_ENVIRONMENT_FACTS=1")
		}
		if !slices.Equal(got, want) || len(inherited) != 3 {
			t.Fatalf("installed=%t: %q", installed, got)
		}
	}
}

func TestEnvironmentFactsCompactionProviderInput(t *testing.T) {
	workspace := gitTestWorkspace(t)
	writeTestFile(t, filepath.Join(workspace, "tracked"), "base")
	gitTestCommit(t, workspace)
	proxy := newManagedMekugiProxy(t)
	attachTestReplayStore(t, proxy)
	headers := serverMetadataHeaders(t, "turn", map[string]jsonv1.RawMessage{workspace: nil})
	forward := func(reference string) string {
		request := serverRequest(t, func(fields map[string]any) {
			if reference != "" {
				fields["input"] = append(fields["input"].([]any), map[string]string{"type": "compaction", "encrypted_content": reference})
			}
		})
		provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed"}`)}}}
		var output bytes.Buffer
		if err := executeRequest(t.Context(), t.Context(), request, headers, "compaction-facts", provider, &output, nil, proxy); err != nil {
			t.Fatal(err)
		}
		return environmentFacts(t, provider.forwarded[0])
	}
	first := forward("")
	writeTestFile(t, filepath.Join(workspace, "first"), "new")
	reference, err := moduletest.StoreCompact(t.Context(), proxy.retained, "thread-1", "retained handover", "")
	if err != nil {
		t.Fatal(err)
	}
	compacted := forward(reference)
	if !strings.Contains(compacted, "\n?? first\n") || compacted == first {
		t.Fatal("applied compaction did not refresh facts")
	}
	writeTestFile(t, filepath.Join(workspace, "second"), "new")
	repeated := forward(reference)
	if repeated != compacted {
		t.Fatal("repeated compact reference rescanned")
	}
	reference, err = moduletest.StoreCompact(t.Context(), proxy.retained, "thread-1", "next retained handover", "")
	if err != nil {
		t.Fatal(err)
	}
	if refreshed := forward(reference); !strings.Contains(refreshed, "\n?? first\n?? second\n") {
		t.Fatal("later compaction reused snapshot")
	}
}

func TestEnvironmentFactsFreshStartAdmission(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.controlObservations = new(sessioncontrol.Observations)
	u.Thread = ""
	appServerTestRequest(t, u, "\"fresh-environment\"", "thread/start")
	if u.controlObservations.ContextRevision("fresh") != 0 {
		t.Fatal("request issuance refreshed before host acceptance")
	}
	appServerTestMessage(t, u, `{"id":"fresh-environment","result":{"thread":{"id":"fresh","cwd":"/tmp"}}}`)
	if u.controlObservations.ContextRevision("fresh") != 1 {
		t.Fatal("fresh host acceptance did not refresh")
	}
	appServerTestMessage(t, u, `{"id":"fresh-environment","result":{"thread":{"id":"fresh","cwd":"/tmp"}}}`)
	if u.controlObservations.ContextRevision("fresh") != 1 {
		t.Fatal("duplicate host response refreshed again")
	}
}

func TestEnvironmentFactsNativeResetProviderInput(t *testing.T) {
	workspace := gitTestWorkspace(t)
	proxy := newManagedMekugiProxy(t)
	u, _ := newAppServerTestUI()
	u.controlObservations = proxy.control
	headers := serverMetadataHeaders(t, "turn", map[string]jsonv1.RawMessage{workspace: nil})
	forward := func(window string) string {
		var metadata map[string]any
		if err := json.Unmarshal([]byte(headers.Get(codexresponses.TurnMetadataHeader)), &metadata); err != nil {
			t.Fatal(err)
		}
		metadata["context_window_id"] = window
		headers.Set(codexresponses.TurnMetadataHeader, string(responses.MustMarshalJSON(metadata)))
		provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed"}`)}}}
		var output bytes.Buffer
		if err := executeRequest(t.Context(), t.Context(), serverRequest(t, nil), headers, "reset-facts", provider, &output, nil, proxy); err != nil {
			t.Fatal(err)
		}
		return environmentFacts(t, provider.forwarded[0])
	}
	first := forward("initial-window")
	writeTestFile(t, filepath.Join(workspace, "after-reset"), "new")
	notify := func(status string) {
		appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "thread-1", "turnId": "reset", "item": binding.ThreadItem{ID: "reset", Type: "contextCompaction", Status: status}})
	}
	notify("failed")
	if forward("initial-window") != first {
		t.Fatal("failed compaction refreshed facts")
	}
	// Request admission precedes the queued presentation completion.
	refreshed := forward("reset-window")
	if refreshed == first || !strings.Contains(refreshed, "?? after-reset") {
		t.Fatal("native reset did not refresh facts")
	}
	writeTestFile(t, filepath.Join(workspace, "later"), "new")
	notify("completed")
	if forward("reset-window") != refreshed {
		t.Fatal("duplicate reset rescanned facts")
	}
}

func TestEnvironmentFactsRewindProviderInput(t *testing.T) {
	for _, compactBeforeTurn := range []bool{false, true} {
		t.Run(strconv.FormatBool(compactBeforeTurn), func(t *testing.T) {
			workspace := gitTestWorkspace(t)
			proxy := newManagedMekugiProxy(t)
			headers := serverMetadataHeaders(t, "turn", map[string]jsonv1.RawMessage{workspace: nil})
			forward := func(window string) string {
				metadata, _ := codexresponses.DecodeTurnMetadata(headers)
				metadata.ContextWindowID = window
				headers.Set(codexresponses.TurnMetadataHeader, string(responses.MustMarshalJSON(metadata)))
				provider := &serverFakeProvider{results: []serverForwardResult{{response: serverHTTPResponse(`{"status":"completed"}`)}}}
				var output bytes.Buffer
				if err := executeRequest(t.Context(), t.Context(), serverRequest(t, nil), headers, "rewind-facts", provider, &output, nil, proxy); err != nil {
					t.Fatal(err)
				}
				return environmentFacts(t, provider.forwarded[0])
			}
			first := forward("initial")
			writeTestFile(t, filepath.Join(workspace, "after-rewind"), "new")
			before := proxy.control.ContextRevision("thread-1")
			proxy.control.RewindContext("thread-1", workspace)
			if compactBeforeTurn {
				metadata, _ := codexresponses.DecodeTurnMetadata(serverCompactionMetadataHeaders(t))
				metadata.ThreadID = "thread-1"
				metadata.Directories = map[string]jsonv1.RawMessage{workspace: nil}
				request := serverRequest(t, func(fields map[string]any) {
					fields["stream"] = true
					fields["parallel_tool_calls"] = false
					fields["tools"] = []any{}
					fields["input"] = []any{map[string]string{"role": "user", "content": "Summarize."}}
				})
				_, err := proxy.preparer().Prepare(t.Context(), &request, requestexecution.Preparation{Session: "rewind-facts", Thread: "thread-1", Metadata: metadata, MetadataValid: true})
				if err != nil {
					t.Fatal(err)
				}
			}
			next := forward("reloaded")
			if (next == first) == compactBeforeTurn {
				t.Fatalf("rewind reuse=%t after compaction=%t", next == first, compactBeforeTurn)
			}
			if proxy.control.ContextRevision("thread-1") <= before {
				t.Fatal("environment reuse suppressed history reset identity")
			}
			if forward("reloaded") != next {
				t.Fatal("ordinary request refreshed rebound facts")
			}
			writeTestFile(t, filepath.Join(workspace, "after-reset"), "new")
			if refreshed := forward("later-reset"); refreshed == next || !strings.Contains(refreshed, "after-reset") {
				t.Fatal("later native reset did not refresh workspace facts")
			}
		})
	}
}
