package router

import (
	"cmp"
	json "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/yusing/mekugi/internal/orchestrate"
)

// Orchestrated children are independent threads, not native subagents.
type orchestrateSpawnInput struct {
	TaskName        string  `json:"task_name"`
	Message         string  `json:"message"`
	Model           string  `json:"model,omitempty"`
	ReasoningEffort string  `json:"reasoning_effort,omitempty"`
	ServiceTier     *string `json:"service_tier,omitempty"`
}

// Source: codex-rs/app-server-protocol/src/protocol/v2/thread.rs:62:120@823ea830c
// Build from host-confirmed live settings, not invocation defaults. The caller
// retains this request before dispatch and records the effective host response.
func (u *appServerUI) orchestrateThreadRequest(batch orchestrate.Batch, input orchestrateSpawnInput) (map[string]any, error) {
	if u.thread == "" || u.settings.pending() || u.replacement.pending() || u.restoring != nil {
		return nil, errors.New("wait for the coordinator's thread settings to settle")
	}
	var profile struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(u.statusConfig.PermissionProfile, &profile) != nil || profile.ID == "" {
		return nil, errors.New("coordinator permission profile is unavailable; cannot inherit permissions")
	}
	if !u.statusConfig.Approval.IsValid() || u.statusConfig.Approval.Kind() == 'n' || u.statusConfig.ApprovalsReviewer == "" || u.statusConfig.Provider == "" || u.model == "" {
		return nil, errors.New("coordinator launch settings are incomplete")
	}
	roots, err := orchestrateWorkspaceRoots(u.session.cwd, batch, u.statusConfig.WorkspaceRoots)
	if err != nil {
		return nil, err
	}
	params := map[string]any{
		"cwd": batch.Cwd, "runtimeWorkspaceRoots": roots,
		"model": cmp.Or(input.Model, u.model), "modelProvider": u.statusConfig.Provider,
		"approvalPolicy":    slices.Clone(u.statusConfig.Approval),
		"approvalsReviewer": u.statusConfig.ApprovalsReviewer, "permissions": profile.ID,
		"serviceTier": nil,
	}
	tier := u.serviceTier
	if input.ServiceTier != nil {
		tier = *input.ServiceTier
	}
	if tier != "" {
		params["serviceTier"] = tier
	}
	// Resolve the live model default explicitly. A null config override becomes
	// an empty TOML string in Codex and cannot clear a stale invocation setting.
	effort := cmp.Or(input.ReasoningEffort, u.reasoningEffort)
	if effort == "" {
		if model := u.currentModel(); model != nil {
			effort = model.DefaultEffort
		}
		if effort == "" {
			return nil, errors.New("wait for the coordinator's model reasoning default")
		}
	}
	params["config"] = map[string]any{"model_reasoning_effort": effort}
	return params, nil
}

func orchestrateWorkspaceRoots(sourceCwd string, batch orchestrate.Batch, roots []string) ([]string, error) {
	if !filepath.IsAbs(sourceCwd) || !filepath.IsAbs(batch.Checkout) || !filepath.IsAbs(batch.Cwd) {
		return nil, errors.New("launch requires absolute source and prepared workspace paths")
	}
	relative, err := filepath.Rel(batch.Checkout, batch.Cwd)
	if err != nil || !filepath.IsLocal(relative) {
		return nil, errors.New("prepared cwd is outside its checkout")
	}
	sourceCwd, err = filepath.EvalSymlinks(sourceCwd)
	if err != nil {
		return nil, err
	}
	sourceRoot := sourceCwd
	for dir := relative; dir != "."; dir = filepath.Dir(dir) {
		sourceRoot = filepath.Dir(sourceRoot)
	}
	if filepath.Join(sourceRoot, relative) != filepath.Clean(sourceCwd) {
		return nil, errors.New("prepared cwd does not match the selected source directory")
	}
	mapped := make([]string, 0, len(roots))
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			return nil, errors.New("coordinator workspace root is not absolute")
		}
		// Match preparation's physical source identity, including roots whose
		// final directories will be created later under an existing symlink.
		physical, suffix := root, ""
		for {
			resolved, err := filepath.EvalSymlinks(physical)
			if err == nil {
				physical = filepath.Join(resolved, suffix)
				break
			}
			if !os.IsNotExist(err) || filepath.Dir(physical) == physical {
				return nil, err
			}
			if _, statErr := os.Lstat(physical); statErr == nil {
				return nil, err // A dangling link is not a missing directory.
			}
			suffix = filepath.Join(filepath.Base(physical), suffix)
			physical = filepath.Dir(physical)
		}
		if rel, err := filepath.Rel(sourceRoot, physical); err == nil && filepath.IsLocal(rel) {
			root = filepath.Join(batch.Checkout, rel)
		}
		if !slices.Contains(mapped, root) {
			mapped = append(mapped, root)
		}
	}
	return mapped, nil
}
