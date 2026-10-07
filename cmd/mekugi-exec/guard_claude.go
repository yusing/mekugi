package main

import (
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/internal/execsegment"
	"github.com/yusing/mekugi/internal/vcsguard"
)

// The official exec-form hook receives native's materialized child environment,
// without evaluating a shell startup file or changing the native tool input.
func nativeGuardEnvironment(startup, receipts string, input io.Reader) (result error) {
	var hook struct {
		Event string `json:"hook_event_name"`
		ID    string `json:"tool_use_id"`
		Tool  string `json:"tool_name"`
	}
	if err := json.UnmarshalRead(io.LimitReader(input, 8<<20), &hook); err != nil {
		return errors.New("native startup hook input unavailable")
	}
	name := "startup"
	if hook.Event == "PreToolUse" && hook.Tool == "Bash" && hook.ID != "" {
		name = fmt.Sprintf("%x", sha256.Sum256([]byte(hook.ID)))
	} else if hook.Event != "SessionStart" {
		return errors.New("native startup hook identity unavailable")
	}
	// Only an atomic success receipt admits effects. A denial receipt carries a
	// fixed diagnostic, never caller environment contents.
	defer func() {
		message := "ok"
		if result != nil {
			message = "deny: " + result.Error()
		}
		file, err := os.CreateTemp(receipts, ".receipt-")
		if err != nil {
			result = errors.New("native startup receipt unavailable")
			return
		}
		defer os.Remove(file.Name())
		_, writeErr := file.WriteString(message)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			result = errors.New("native startup receipt unavailable")
			return
		}
		if err := os.Rename(file.Name(), filepath.Join(receipts, name)); err != nil {
			result = errors.New("native startup receipt unavailable")
			return
		}
	}()
	if os.Getenv("CLAUDE_CODE_SHELL_PREFIX") != "" {
		return errors.New("native shell prefix can replace the startup observer")
	}
	if os.Getenv("BASH_ENV") != startup || startup == "" {
		return errors.New("native settings replaced the Bash startup observer")
	}
	for _, path := range []string{startup, filepath.Join(filepath.Dir(startup), "exec-track.bash")} {
		file, err := os.Open(path)
		if err != nil {
			return errors.New("native startup resource unavailable")
		}
		info, err := file.Stat()
		file.Close()
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return errors.New("native startup resource unavailable")
		}
	}
	selected := os.Getenv("CLAUDE_CODE_SHELL")
	if selected == "" {
		selected = os.Getenv("SHELL")
	}
	resolved, err := filepath.EvalSymlinks(selected)
	if err != nil || !filepath.IsAbs(selected) || !strings.Contains(filepath.Base(resolved), "bash") {
		return errors.New("native child environment does not select a usable Bash shell")
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() || info.Mode()&0111 == 0 {
		return errors.New("native Bash selection is unavailable")
	}
	if _, set := os.LookupEnv(execsegment.Guard); set {
		return errors.New("native child environment disables the startup observer")
	}
	if hook.Event == "PreToolUse" && os.Getenv("CLAUDE_ENV_FILE") != "" {
		return errors.New("unowned native session environment can replace the startup observer")
	}
	return nil
}

// An unsupported or ambiguous observer claim still guards the native wrapper
// without assigning segment evidence to a guessed native tool identity.
func nativeGuardFallback(wrapper, directory string) {
	fail := func() { fmt.Println("deny") }
	script, ok := execsegment.ClaudePayload(wrapper)
	if !ok {
		fail()
		return
	}
	helper, err := os.Executable()
	if err != nil {
		fail()
		return
	}
	guarded, err := vcsguard.Rewrite(script, helper, directory)
	if err != nil {
		fail()
		return
	}
	if guarded == script {
		fmt.Println("pass")
		return
	}
	rewritten, ok := execsegment.ClaudeExecutionWrapper(wrapper, guarded)
	if !ok {
		fail()
		return
	}
	work, err := os.MkdirTemp(directory, ".run-")
	if err != nil {
		fail()
		return
	}
	defer os.RemoveAll(work)
	if err := os.WriteFile(filepath.Join(work, "script"), []byte(rewritten), 0600); err != nil {
		fail()
		return
	}
	fmt.Println("guard " + work)
	// Startup reads the script before closing its coprocess descriptors.
	io.Copy(io.Discard, os.Stdin)
}
