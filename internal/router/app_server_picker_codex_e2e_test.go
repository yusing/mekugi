//go:build journal_e2e

package router

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Installed Codex owns both searches. The UI only renders their app-server
// results and inserts selections; this fixture never submits a model turn.
func TestComposerPickersNativeCodexE2E(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	fileName := "picker-unique-document-4827.txt"
	ignoredName := "picker-excluded-document-4827.txt"
	gitName := "picker-git-hidden-4827.txt"
	svnName := "picker-svn-hidden-4827.txt"
	hgName := "picker-hg-hidden-4827.txt"
	skillName := "picker-unique-skill-4827"
	if err := os.WriteFile(filepath.Join(workspace, fileName), []byte("file picker fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		".gitignore":                             ignoredName + "\n",
		ignoredName:                              "excluded file fixture\n",
		".git/HEAD":                              "ref: refs/heads/main\n",
		".git/config":                            "[core]\n\trepositoryformatversion = 0\n\tbare = false\n",
		filepath.Join(".git", gitName):           "git metadata fixture\n",
		filepath.Join("nested", ".svn", svnName): "svn metadata fixture\n",
		filepath.Join("nested", ".hg", hgName):   "hg metadata fixture\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(workspace, path)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{".git/objects", ".git/refs/heads"} {
		if err := os.MkdirAll(filepath.Join(workspace, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	skillDir := filepath.Join(workspace, ".agents", "skills", skillName)
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: "+skillName+"\ndescription: Unique picker acceptance fixture\n---\n\nFixture skill.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	provider := &appResumeProvider{}
	server := httptest.NewServer(responsesHandler(t.Context(), time.Minute, provider, nil, nil))
	defer server.Close()
	environment := routerFaultCodexEnvironment(t)
	newCommand := func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, codex, "app-server", "-c", `model_providers.preview={name="preview",base_url=`+strconv.Quote(server.URL+"/v1")+`,wire_api="responses",requires_openai_auth=false}`, "-c", `model_provider="preview"`, "-c", `model="gpt-6-astra"`, "-c", "features.plugins=false", "-c", "include_collaboration_mode_instructions=false")
		cmd.Env, cmd.Dir = environment, workspace
		return cmd
	}
	terminal := startAppResumeTerminal(t, newCommand, "")
	terminal.await("Ready")

	terminal.send("@picker-unique-document")
	terminal.awaitMatch("live file search", func(screen string) bool {
		return strings.Contains(screen, "@! include excluded files") && strings.Contains(screen, fileName)
	})
	terminal.send("\t")
	terminal.awaitMatch("inserted file, not submitted", func(screen string) bool {
		return strings.Contains(screen, "@"+fileName+" ") && !strings.Contains(screen, "@! include excluded files")
	})
	terminal.send("\x7f\x7f")
	terminal.awaitMatch("file token deleted as one unit", func(screen string) bool {
		return !strings.Contains(screen, fileName) && !strings.Contains(screen, "@! include excluded files")
	})
	terminal.send("$picker-unique-skill")
	terminal.awaitMatch("live enabled skill search", func(screen string) bool {
		return strings.Contains(screen, "enter insert · esc close") && strings.Contains(screen, skillName)
	})
	terminal.send("\t")
	terminal.awaitMatch("inserted skill, not submitted", func(screen string) bool {
		return strings.Contains(screen, "$"+skillName+" ") && !strings.Contains(screen, "enter insert · esc close")
	})
	terminal.send("\x03")
	for _, fixture := range []struct {
		query, found string
		included     bool
	}{
		{"picker-excluded-document", ignoredName, true},
		{"picker-git-hidden", filepath.Join(".git", gitName), false},
		{"picker-svn-hidden", filepath.Join("nested", ".svn", svnName), false},
		{"picker-hg-hidden", filepath.Join("nested", ".hg", hgName), false},
	} {
		terminal.send("@" + fixture.query)
		terminal.awaitMatch("excluded from ordinary @ search", func(screen string) bool {
			return strings.Contains(screen, "@! include excluded files") && strings.Contains(screen, "no matches") && strings.Contains(screen, "@"+fixture.query) && !strings.Contains(screen, fixture.found)
		})
		terminal.send("\x03@!" + fixture.query)
		if fixture.included {
			terminal.awaitMatch("ignored ordinary file included in @! search", func(screen string) bool {
				return strings.Contains(screen, "@! include excluded files") && strings.Contains(screen, fixture.found)
			})
			terminal.send("\t")
			terminal.awaitMatch("included file inserted", func(screen string) bool {
				return strings.Contains(screen, "@"+fixture.found+" ") && !strings.Contains(screen, "@! include excluded files")
			})
		} else {
			terminal.awaitMatch("VCS metadata excluded even from @! search", func(screen string) bool {
				return strings.Contains(screen, "@! include excluded files") && strings.Contains(screen, "no matches") && strings.Contains(screen, "@!"+fixture.query) && !strings.Contains(screen, fixture.found)
			})
		}
		terminal.send("\x03")
	}
	terminal.send("@picker-unique-document")
	terminal.await("@! include excluded files")
	terminal.send("\x1b")
	terminal.awaitMatch("Escape preserves token", func(screen string) bool {
		return strings.Contains(screen, "@picker-unique-document") && !strings.Contains(screen, "@! include excluded files")
	})
	terminal.send("\x03/skills\r")
	terminal.awaitMatch("skills action menu", func(screen string) bool {
		return strings.Contains(screen, "1. List skills") && strings.Contains(screen, "2. Enable/Disable Skills")
	})
	terminal.send("\r")
	terminal.awaitMatch("list action inserts skill trigger", func(screen string) bool {
		return strings.Contains(screen, "enter insert · esc close") && !strings.Contains(screen, "1. List skills")
	})
	terminal.send("\x03/skills\r")
	terminal.await("2. Enable/Disable Skills")
	terminal.send("2")
	terminal.await("Turn skills on or off")
	terminal.send(skillName)
	terminal.awaitMatch("enabled skill in manager", func(screen string) bool {
		return strings.Contains(screen, "[x] "+skillName)
	})
	terminal.send(" ")
	terminal.awaitMatch("skill disabled through Codex", func(screen string) bool {
		return strings.Contains(screen, "[ ] "+skillName) && !strings.Contains(screen, "Saving…")
	})
	terminal.send("\x1b")
	terminal.awaitMatch("management closed", func(screen string) bool {
		return !strings.Contains(screen, "Enable/Disable Skills")
	})
	terminal.send("/skills\r")
	terminal.await("2. Enable/Disable Skills")
	terminal.send("2")
	terminal.await("Turn skills on or off")
	terminal.send(skillName)
	terminal.awaitMatch("disabled state persists after reload", func(screen string) bool {
		return strings.Contains(screen, "[ ] "+skillName)
	})
	terminal.send(" ")
	terminal.awaitMatch("skill re-enabled through Codex", func(screen string) bool {
		return strings.Contains(screen, "[x] "+skillName) && !strings.Contains(screen, "Saving…")
	})
	terminal.send("\x1b")
	terminal.awaitMatch("management closed again", func(screen string) bool {
		return !strings.Contains(screen, "Enable/Disable Skills")
	})
	terminal.send("\x022")
	terminal.await("2 Diff")
	terminal.send("\x021")
	terminal.await("1 Main")
	if got := provider.snapshot(); len(got) != 0 {
		t.Fatalf("picker invoked provider without submission: %q", got)
	}
	terminal.send("$" + skillName + " ")
	terminal.awaitMatch("automatically attached exact skill", func(screen string) bool {
		return !strings.Contains(screen, "enter insert · esc close") && terminalSkillIsAmber(terminal, "$"+skillName)
	})
	terminal.send("\r")
	terminal.await("Recovered after a retry.")
	terminal.await("completed")
	if !terminalSkillIsAmber(terminal, "$"+skillName) {
		t.Fatal("host echo lost amber skill identity")
	}
	terminal.quit()
	resumed := startAppResumeTerminal(t, newCommand, "--last")
	resumed.await("Ready")
	resumed.awaitMatch("restored amber skill", func(screen string) bool {
		return strings.Contains(screen, skillName) && terminalSkillIsAmber(resumed, "$"+skillName)
	})
	if got := provider.snapshot(); len(got) != 1 {
		t.Fatalf("resume resent skill input: %q", got)
	}
	resumed.quit()
}

func terminalSkillIsAmber(terminal *appResumeTerminal, label string) bool {
	r, g, b, _ := terminal.screen.IndexedColor(214).RGBA()
	for y := 0; y < terminal.screen.Height(); y++ {
		for x := 0; x+len(label) <= terminal.screen.Width(); x++ {
			match := true
			for i, char := range label {
				cell := terminal.screen.CellAt(x+i, y)
				if cell == nil || cell.Content != string(char) || cell.Style.Fg == nil {
					match = false
					break
				}
				cr, cg, cb, _ := cell.Style.Fg.RGBA()
				if cr != r || cg != g || cb != b {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}
	return false
}
