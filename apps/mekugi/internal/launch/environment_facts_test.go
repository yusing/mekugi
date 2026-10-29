package launch

import (
	"bytes"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/executionobservation"
	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func environmentFacts(t *testing.T, raw []byte) string {
	t.Helper()
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	instructions := responses.StringField(fields, "instructions")
	if strings.Count(instructions, "<mekugi_environment_facts>") != 1 {
		t.Fatal("provider input must contain one environment supplement")
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
	if strings.Contains(responses.StringField(request.Fields, "instructions"), "<mekugi_environment_facts>") {
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

func TestEnvironmentCapabilityShellOverride(t *testing.T) {
	forwarded := []string{"exec", "-c", `shell_environment_policy.set.UNRELATED="kept"`, "-c", `shell_environment_policy.set.MEKUGI_ENVIRONMENT_FACTS="1"`, "--", "prompt"}
	args := codexArgs("http://localhost/v1", forwarded, false, true)
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
	owner := executionobservation.New(executionobservation.Config{Retained: proxy.retained})
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
	reference, err := owner.StoreCompact(t.Context(), "thread-1", "retained handover", "")
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
	reference, err = owner.StoreCompact(t.Context(), "thread-1", "next retained handover", "")
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
