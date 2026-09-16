package codexinstructions

import (
	"strings"
	"testing"
)

func TestHelpTopicsOwnDeferredSyntax(t *testing.T) {
	index, ok := Help("")
	if !ok || !strings.Contains(index, "hhelp TOPIC") {
		t.Fatal("missing help index")
	}
	for topic, required := range map[string][]string{
		"shell":    {"#!batch=SEPARATOR", "#!batch-stop=SEPARATOR", "#!cmd=", "#!script=@shell/"},
		"read":     {"--preview-bytes N", "--stdout", "--stderr", "--workspace ROOT", "not `| tail`", "14 days"},
		"journal":  {"journal list [AGENT]", "journal delete ID", "journal batch JSON_ARRAY", "--clear-answer", "--json", "omit `answer` to preserve"},
		"recovery": {"HANDLE target TARGET", "HANDLE value VALUE", "maple \"return oldResult, nil\"", "type \"bad value\" \"fixed value\"", "keep the two payload forms separate", "use its script rows and refreshed command handles"},
		"changes":  {"--summary", "--history", "--workspace DIR", "[-- PATH ...]", "not a combined net change"},
	} {
		t.Run(topic, func(t *testing.T) {
			content, ok := Help(topic)
			if !ok || !strings.Contains(index, topic) {
				t.Fatal("topic unavailable or absent from index")
			}
			// Each bounded topic fits even a conservative one-token-per-byte 4k budget.
			if len(content) > 4000 {
				t.Fatalf("topic exceeds a normal help read: %d bytes", len(content))
			}
			for _, fragment := range required {
				if !strings.Contains(instructionWords(content), fragment) {
					t.Errorf("missing deferred detail %q", fragment)
				}
			}
			for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
				for _, compact := range []bool{false, true} {
					prompt := InstructionsForModel(model, compact)
					if !strings.Contains(prompt, "hhelp "+topic) || strings.Contains(prompt, content) {
						t.Fatal("topic must be discoverable, not always injected")
					}
				}
			}
		})
	}
	for _, invalid := range []string{"unknown", "../instructions", "/shell", "shell.md", "shell/read"} {
		if content, ok := Help(invalid); ok || content != "" {
			t.Errorf("unknown topic %q returned help", invalid)
		}
	}
}
