package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDocumentationLinks(t *testing.T) {
	for _, test := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"local and reference links", map[string]string{
			"README.md":     "[guide](docs/guide.md#usage)\n[asset][picture]\n[picture]: image.svg\n",
			"docs/guide.md": "# Usage\n", "image.svg": "<svg/>"}, ""},
		{"missing target", map[string]string{"README.md": "[missing](absent.md)"}, "absent.md"},
		{"repository root link", map[string]string{"README.md": "# Root\n", "docs/guide.md": "[root](/README.md#root)"}, ""},
		{"escaped destination", map[string]string{"README.md": `[target](a\(b\).md#target)`, "a(b).md": "# Target\n"}, ""},
		{"entity destination", map[string]string{"README.md": "[target](a&amp;b.md#target)", "a&b.md": "# Target\n"}, ""},
		{"missing anchor", map[string]string{"README.md": "[missing](#absent)"}, "missing heading anchor"},
		{"fenced and inline examples", map[string]string{"README.md": "`[example](absent.md)`\n\n```md\n[example](absent.md)\n```\n"}, ""},
		{"headings", map[string]string{"README.md": "# *Hello* `世界` &amp; [friends](#hello-世界--friends)\n\nRepeat\n======\n\n# Repeat\n\n[duplicate](#repeat-1)\n"}, ""},
		{"heading collision", map[string]string{"README.md": "# Name\n# Name-1\n# Name\n[duplicate](#name-2)\n"}, ""},
		{"heading tab", map[string]string{"README.md": "# Hello\tWorld\n[heading](#helloworld)"}, ""},
		{"literal code entity", map[string]string{"README.md": "# `&amp;`\n[heading](#amp)"}, ""},
		{"multiline heading", map[string]string{"README.md": "Hello\nworld\n=====\n[heading](#hello-world)"}, ""},
		{"custom anchors", map[string]string{"README.md": "<a name=example></a>\n\n[custom](#example) [inline](#inline)\n\nText <a id='inline'></a>.\n"}, ""},
		{"custom anchor numbering", map[string]string{"README.md": "<a name=title></a>\n\n# Title\n\n# Title\n\n[numbered](#title-1)\n"}, ""},
		{"custom anchor does not add a heading", map[string]string{"README.md": "<a name=title></a>\n\n# Title\n\n# Title\n\n[absent](#title-2)\n"}, "missing heading anchor"},
		{"spaces and encoding", map[string]string{"README.md": "[file](<a file.md#two%20words>)", "a file.md": "# Two words\n"}, "missing heading anchor"},
		{"encoded path", map[string]string{"README.md": "[file](a%20file.md?raw=1#two-words)", "a file.md": "# Two words\n"}, ""},
		{"external links", map[string]string{"README.md": "[web](https://example.com/missing) [mail](mailto:example@example.com) [cdn](//example.com/file)"}, ""},
		{"missing image", map[string]string{"README.md": "![image](missing.png)"}, "missing.png"},
		{"excluded fixtures", map[string]string{
			"README.md": "# Guide\n", "testdata/bad.md": "[bad](absent)", "node_modules/bad.md": "[bad](absent)",
			".worktrees/bad.md": "[bad](absent)", "build/bad.md": "[bad](absent)", "vendor/bad.md": "[bad](absent)"}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			for name, body := range test.files {
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := checkDocs(root)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("checkDocs() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDocumentationCommand(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("README.md", []byte("# Guide\n[missing](absent.md)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"docs-check"}); err == nil || !strings.Contains(err.Error(), "absent.md") {
		t.Fatalf("docs-check = %v, want missing document", err)
	}
	if err := run([]string{"docs-check", "extra"}); err == nil {
		t.Fatal("docs-check accepted an extra argument")
	}
}

func TestDocumentationGitDiscovery(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_DIR", filepath.Join(root, ".git"))
	t.Setenv("GIT_WORK_TREE", root)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(root, ".git", "index"))
	t.Setenv("GIT_COMMON_DIR", filepath.Join(root, ".git"))
	if out, err := exec.Command("git", "-C", root, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("init fixture: %v: %s", err, out)
	}
	for name, body := range map[string]string{
		".gitignore": "ignored.md\ntracked.md\n", "ignored.md": "[bad](absent)", "README.md": "# Guide\n", "tracked.md": "[bad](absent)",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkDocs(root); err != nil {
		t.Fatalf("ignored scratch was checked: %v", err)
	}
	if out, err := exec.Command("git", "-C", root, "add", "-f", "tracked.md").CombinedOutput(); err != nil {
		t.Fatalf("stage ignored document: %v: %s", err, out)
	}
	if err := checkDocs(root); err == nil || !strings.Contains(err.Error(), "tracked.md") {
		t.Fatalf("tracked ignored document was skipped: %v", err)
	}
}
