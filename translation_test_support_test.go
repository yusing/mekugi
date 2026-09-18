package mekugi

import "context"

func translateForTest(ctx context.Context, workspace Workspace, edits []FileEdit) ([]byte, error) {
	changes, _, report, aliases, err := evaluateScript(ctx, workspace, edits)
	if err != nil {
		return nil, err
	}
	result := hostTranslationResult(changes, report, aliases, true)
	if err := translateHostResult(ctx, changes, &result); err != nil {
		return nil, err
	}
	return result.Patch, nil
}

func translateForHostForTest(ctx context.Context, workspace Workspace, edits []FileEdit, dataDirectory string) (HostTranslation, error) {
	changes, _, report, aliases, err := evaluateScript(ctx, workspace, edits)
	result := hostTranslationResult(changes, report, aliases, err == nil)
	failureStage := ""
	if err != nil {
		failureStage = "evaluated"
	} else if err = translateHostResult(ctx, changes, &result); err != nil {
		failureStage = "translated"
	}
	return finishHostChange(ctx, dataDirectory, joinedFileEditScripts(edits), result, failureStage, err, false)
}
