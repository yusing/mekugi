package sessioncontrol

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func (r *Requests[T]) ReadMessageSources(ctx context.Context, member *control.Member, selection string) (string, error) {
	settings := member.Settings
	if member.HostRunning && member.Turn != "" {
		current, err := binding.ReadRuntimeContext(member.Thread, member.Path, member.Turn)
		if err != nil {
			return "", err
		}
		settings.Workspace, settings.PermissionProfile = current.Workspace, current.PermissionProfile
		settings.PermissionID = ""
		if current.ActivePermissionProfile != nil {
			settings.PermissionID = current.ActivePermissionProfile.ID
		}
	} else if member.Turn != "" {
		// An idle thread can apply new permissions without producing another
		// turn_context. Its last turn must not restore superseded authority.
		current, err := binding.ReadAdmissionSettings(member.Thread, member.Path)
		if err != nil {
			return "", err
		}
		settings.Workspace, settings.PermissionProfile = current.Workspace, current.PermissionProfile
		settings.PermissionID = ""
		if current.ActivePermissionProfile != nil {
			settings.PermissionID = current.ActivePermissionProfile.ID
		}
	}
	permissions := map[string]any{}
	if settings.PermissionID != "" {
		permissions["permissionProfile"] = settings.PermissionID
	} else {
		var profile struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(settings.PermissionProfile, &profile); err != nil || profile.Type != "disabled" {
			return "", errors.New("recipient permission profile has no host-selectable identity")
		}
		permissions["sandboxPolicy"] = map[string]string{"type": "dangerFullAccess"}
	}
	snapshot, err := compact.ReadSourcesWithRunner(ctx, selection, settings.Workspace, func(ctx context.Context, script string, limit int) (compact.CapturedSource, error) {
		params := map[string]any{
			"command": []string{"bash", "-c", script}, "cwd": settings.Workspace,
			"outputBytesCap": limit + 1,
			"env":            map[string]any{"BASH_ENV": nil, "MEKUGI_EXEC_TRACK": nil},
		}
		for key, value := range permissions {
			params[key] = value
		}
		raw, err := r.call(ctx, member.Thread, "command/exec", params, nil)
		var result struct {
			Stdout   string `json:"stdout"`
			Stderr   string `json:"stderr"`
			ExitCode *int   `json:"exitCode"`
		}
		if err == nil {
			err = json.Unmarshal(raw, &result)
		}
		if err == nil && result.ExitCode == nil {
			err = errors.New("native source reader omitted its exit status")
		}
		if err == nil && len(result.Stdout)+len(result.Stderr) > limit {
			err = fmt.Errorf("message source exceeds remaining capture budget of %d bytes", limit)
		}
		code := 1
		if result.ExitCode != nil {
			code = *result.ExitCode
		}
		return compact.CapturedSource{Stdout: result.Stdout, Stderr: result.Stderr, ExitCode: code}, err
	})
	return snapshot.AttachmentText(), err
}
