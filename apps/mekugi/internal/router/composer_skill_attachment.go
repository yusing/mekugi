package router

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
	"unicode/utf8"

	conversationmodel "github.com/yusing/mekugi/modules/conversation/presentation"

	"github.com/yusing/mekugi/modules/conversation/composer"

	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
)

type managedSkillOutput struct{ bytes.Buffer }

func (b *managedSkillOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > attachments.Budget/2 {
		return 0, fmt.Errorf("skill exceeds attachment limit")
	}
	return b.Buffer.Write(data)
}
func readManagedSkill(ctx context.Context, cwd string, environment []string, name string) ([]byte, error) {
	executable, err := managedSkillsExecutable(cwd, environment)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, executable, "get", "--codex", name)
	cmd.Dir, cmd.Env, cmd.WaitDelay = cwd, environment, time.Second
	var output managedSkillOutput
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if !utf8.Valid(output.Bytes()) {
		return nil, fmt.Errorf("skill output is not UTF-8")
	}
	return output.Bytes(), nil
}

// Native selections keep their source path. Managed metadata placeholders
// identify a manager-owned skill, not a file containing its instructions.
func readSkillAttachment(ctx context.Context, cwd string, environment []string, name, path string) ([]byte, bool, error) {
	managed := path == ""
	if path != "" {
		if !filepath.IsAbs(path) {
			return nil, false, fmt.Errorf("skill metadata requires an absolute path")
		}
		_, markerErr := os.Stat(filepath.Join(filepath.Dir(path), ".skills-mgr-placeholder"))
		managed = markerErr == nil
		if markerErr != nil && !os.IsNotExist(markerErr) {
			return nil, false, markerErr
		}
	}
	if managed {
		data, err := readManagedSkill(ctx, cwd, environment, name)
		return data, true, err
	}
	data, err := composer.ReadFile(path)
	return data, false, err
}
func (u *appServerUI) snapshotDraftSkills(d *conversationmodel.Input) {
	d.SnapshotSkills(func(name, path string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		data, _, err := readSkillAttachment(ctx, u.cwd, u.skillEnvironment, name, path)
		return data, err
	})
}
