//go:build codex_e2e

package launch

import (
	"context"
	json "encoding/json/v2"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

func TestComposerFileAttachmentsNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("mekugi-app-server")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	environment, workspace := routerFaultCodexEnvironment(t), t.TempDir()
	const name = "attachment-unique-4827.txt"
	content := strings.Repeat("line 世界\n", 3000)
	path := filepath.Join(workspace, name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &appQueueProvider{requests: make(chan []byte, 8)}
	server := httptest.NewServer(testResponsesHandler(t.Context(), time.Minute, provider, nil, nil))
	t.Cleanup(func() { server.Close() })
	newCommand := func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex,
			"-c", `model_providers.preview={name="preview",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`,
			"-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`,
			"-c", "features.plugins=false")
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}
	check := func(terminal *appResumeTerminal) {
		t.Helper()
		select {
		case body := <-provider.requests:
			var request struct {
				Input []struct {
					Type    string `json:"type"`
					Role    string `json:"role"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"input"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Fatal(err)
			}
			var attached strings.Builder
			promptIndex, sources := -1, 0
			for index, item := range request.Input {
				for _, part := range item.Content {
					if item.Role == "user" && strings.TrimSpace(part.Text) == "Review @"+name {
						promptIndex = index
					}
					for _, source := range attachments.SourceContents(part.Text) {
						if source.Reference == "@"+name {
							if item.Type != "message" || item.Role != "assistant" || part.Type != "output_text" ||
								promptIndex < 0 || index <= promptIndex || source.Error != "" {
								t.Fatalf("source did not follow its input as provider tool context: role=%s type=%s error=%q", item.Role, part.Type, source.Error)
							}
							attached.WriteString(source.Body)
							sources++
						}
					}
				}
			}
			if attached.String() != content || sources != 1 {
				t.Fatalf("native host lost recorded source bytes: %d bytes in %d sources", attached.Len(), sources)
			}
		case <-terminal.ctx.Done():
			t.Fatal("no provider request for file attachment")
		}
	}
	first := startAppResumeTerminal(t, newCommand, "")
	first.await("Ready")
	first.send("Review @attachment-unique")
	first.await(name)
	first.send("\t\r")
	first.await("completed")
	check(first)
	first.await("Attach " + name)
	if strings.Contains(first.screen.String(), "mekugi-file-attachments-v1") {
		t.Fatal("attachment envelope leaked into terminal")
	}
	first.stopCanceled()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// A fresh handler and native process reuse the recorded source without reopening it.
	server.Close()
	server = httptest.NewServer(testResponsesHandler(t.Context(), time.Minute, provider, nil, nil))
	second := startAppResumeTerminal(t, newCommand, "--last")
	second.await("Ready")
	second.await("Attach " + name)
	second.send("Continue from the attached source\r")
	second.await("completed")
	check(second)
	second.quit()
}

// Native and managed instructions share the accepted-input source reader.
func TestSkillContentsNativeCodexE2E(t *testing.T) {
	for _, name := range []string{"native", "managed", "user-invoked"} {
		managed := name != "native"
		hidden := name == "user-invoked"
		t.Run(name, func(t *testing.T) {
			codex, err := exec.LookPath("mekugi-app-server")
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			cache := t.TempDir()
			t.Setenv("XDG_CACHE_HOME", cache)
			workspace := t.TempDir()
			const skill = "attachment-skill-4827"
			const marker = "unique-skill-content-marker-4827"
			const catalogDescription = "managed-catalog-description-4827"
			metadata := "description: Attachment fixture\n"
			if managed {
				metadata = "description: " + catalogDescription + "\n"
			}
			if hidden {
				metadata += "disable-model-invocation: true\n"
			}
			body := "\n" + marker + "\n" + strings.Repeat("Instruction 世界 Δ: check the fixture.\n", 900)
			instructions := "---\nname: " + skill + "\n" + metadata + "---\n" + body
			want := instructions
			if managed {
				want = body
				if hidden {
					want = "---\nname: " + skill + "\ndescription: " + catalogDescription + "\n---\n" + body
				}
			}
			environment := routerFaultCodexEnvironment(t)
			var home string
			for _, entry := range environment {
				if path, ok := strings.CutPrefix(entry, "HOME="); ok {
					home = path
					break
				}
			}
			source := filepath.Join(workspace, ".agents", "skills", skill, "SKILL.md")
			if managed && !hidden {
				// A selected remote skill must win over an unmarked native copy.
				source = filepath.Join(workspace, ".codex", "skills", skill, "SKILL.md")
			}
			if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
				t.Fatal(err)
			}
			native := instructions
			if managed {
				native = "---\nname: " + skill + "\ndescription: Stale native copy\n---\n\nSTALE-NATIVE-CONTENTS\n"
			}
			if hidden {
				native = "---\nname: " + skill + "\ndescription: User-invoked fixture\ndisable-model-invocation: true\n---\n"
				if err := os.WriteFile(filepath.Join(filepath.Dir(source), ".skills-mgr-placeholder"), []byte("skills-mgr remote placeholder\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(source, []byte(native), 0600); err != nil {
				t.Fatal(err)
			}
			manager, managedSource := "", ""
			var proxy *observationFixture
			if managed {
				trust := "[projects." + strconv.Quote(workspace) + "]\ntrust_level = \"trusted\"\n"
				if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(trust), 0600); err != nil {
					t.Fatal(err)
				}
				managedSource = filepath.Join(home, ".skills-mgr", "skills", skill, "SKILL.md")
				selection := `{"schema_revision":1,"skills":{` + strconv.Quote(skill) + `:true}}`
				if !hidden {
					const remote = `"provider":"repository","id":"example-id","name":"` + skill + `","locator":"https://example.invalid/example"`
					const key = "5bcce21409e160edd1439c35cada2399a44b43655f9b06a754e274a350345a69"
					store := filepath.Join(cache, "skills-mgr", "remote-skills")
					managedSource = filepath.Join(store, "content", "generation", "SKILL.md")
					entry := filepath.Join(store, "entries", key+".json")
					if err := os.MkdirAll(filepath.Dir(entry), 0700); err != nil {
						t.Fatal(err)
					}
					record := `{"schemaRevision":1,` + remote + `,"content":"content/generation","fetchedAt":"2026-08-12T12:00:00Z"}`
					if err := os.WriteFile(entry, []byte(record), 0600); err != nil {
						t.Fatal(err)
					}
					selection = `{"schema_revision":3,"skills":{` + strconv.Quote(skill) + `:{"enabled":true,"remote":{` + remote + `}}}}`
				}
				if err := os.MkdirAll(filepath.Dir(managedSource), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(managedSource, []byte(instructions), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(workspace, ".skills-mgr.json"), []byte(selection), 0600); err != nil {
					t.Fatal(err)
				}
				bin := t.TempDir()
				manager = filepath.Join(bin, "skills-mgr")
				catalog := "<skills><skill name=\"" + skill + "\" description=\"Picker-only fixture\"/></skills>"
				if hidden {
					catalog = "<skills/>"
				}
				// The Go picker can list through the CLI, but native attachment acquisition must not.
				script := "#!/bin/sh\nif [ \"$1\" = list ]; then printf '%s' '" + catalog + "'; else exit 42; fi\n"
				if err := os.WriteFile(manager, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				for i, entry := range environment {
					if path, ok := strings.CutPrefix(entry, "PATH="); ok {
						environment[i] = "PATH=" + bin + string(os.PathListSeparator) + path
						break
					}
				}
				proxy = newManagedMekugiProxy(t)
			}
			provider := &appQueueProvider{requests: make(chan []byte, 2)}
			server := httptest.NewServer(testResponsesHandler(t.Context(), time.Minute, provider, nil, nil))
			defer server.Close()
			command := func(ctx context.Context) *exec.Cmd {
				cmd := exec.CommandContext(ctx, codex, "-c", `model_providers.preview={name="preview",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false")
				cmd.Env, cmd.Dir = environment, workspace
				return cmd
			}
			prompt := "Use $" + skill + " then $" + skill + " please"
			integration := testAppServerIntegration(proxy)
			integration.skillsManager = managed
			first := startAppResumeTerminalWithIntegration(t, command, "", integration)
			first.await("Ready")
			first.send("\x1b[200~" + prompt + "\x1b[201~")
			first.awaitMatch("bound skill", func(screen string) bool {
				return strings.Contains(screen, "please") && terminalSkillIsAmber(first, "$"+skill)
			})
			first.send("\r")
			first.await("completed")
			first.await("Attach " + skill)
			if !terminalSkillIsAmber(first, "$"+skill) {
				t.Fatal("committed skill lost highlighting")
			}
			select {
			case body := <-provider.requests:
				var request struct {
					Input []struct {
						Type    string `json:"type"`
						Output  string `json:"output"`
						Role    string `json:"role"`
						Content []struct {
							Type string `json:"type"`
							Text string `json:"text"`
						} `json:"content"`
					} `json:"input"`
				}
				if err := json.Unmarshal(body, &request); err != nil {
					t.Fatal(err)
				}
				var attached strings.Builder
				sources, prompts, markers, managedCatalogs := 0, 0, 0, 0
				for _, item := range request.Input {
					markers += strings.Count(item.Output, marker)
					for _, part := range item.Content {
						if part.Text == prompt {
							prompts++
						}
						markers += strings.Count(part.Text, marker)
						if strings.Contains(part.Text, "STALE-NATIVE-CONTENTS") ||
							(item.Role == "user" && strings.Contains(part.Text, `<skill name="`+skill+`"`)) {
							t.Fatal("redundant native injection reached provider")
						}
						if item.Role == "developer" {
							managedCatalogs += strings.Count(part.Text, catalogDescription)
						}
						for _, source := range attachments.SourceContents(part.Text) {
							if source.Reference == "$"+skill {
								// Standalone native outputs become assistant tool context at the provider boundary.
								if item.Type != "message" || item.Role != "assistant" || part.Type != "output_text" || source.Error != "" {
									t.Fatalf("skill did not reach the provider as tool context: role=%s type=%s error=%q", item.Role, part.Type, source.Error)
								}
								attached.WriteString(source.Body)
								sources++
							}
						}
					}
				}
				if prompts != 1 || sources != 1 || markers != 1 || attached.String() != want {
					t.Fatalf("source missing or duplicated: prompts=%d sources=%d markers=%d bytes=%d", prompts, sources, markers, attached.Len())
				}
				if gotManagedCatalog := managedCatalogs == 1; gotManagedCatalog != (managed && !hidden) || managedCatalogs > 1 {
					t.Fatalf("unexpected managed catalog count: %d", managedCatalogs)
				}
			case <-first.ctx.Done():
				t.Fatal("no skill provider request")
			}
			first.quit()
			if managed {
				if err := os.Remove(manager); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(managedSource); err != nil {
					t.Fatal(err)
				}
				proxy = newManagedMekugiProxy(t)
			}
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			integration = testAppServerIntegration(proxy)
			integration.skillsManager = managed
			resumed := startAppResumeTerminalWithIntegration(t, command, "--last", integration)
			resumed.await("Ready")
			resumed.await("Attach " + skill)
			resumed.awaitMatch("restored skill", func(screen string) bool {
				return strings.Contains(screen, "please") && terminalSkillIsAmber(resumed, "$"+skill)
			})
			select {
			case <-provider.requests:
				t.Fatal("resume resent skill input")
			default:
			}
			resumed.quit()
		})
	}
}
