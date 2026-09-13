package router

import (
	"context"
	"strings"
	"testing"
)

func TestMekugiRecoveryRejectsDifferentWorktree(t *testing.T) {
	calls := 0
	transform, _, _, workspace := newMekugiTestTransform(t, testTranslator(t, &calls))
	transform.visible = map[string]mekugiHistory{"call-old": {
		ToolName: mekugiToolName, Script: testMekugiScript, Root: workspace + "-other", CarrierName: "exec",
		TranslationError: "rejected", sequence: 1,
		EvaluatorRejected: true,
	}}
	history, err := transform.translateRecovery("call-new", recoveryCommands(testMekugiScript)[0].handle+" 1:aaaa", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !history.Unevaluated || !strings.Contains(history.TranslationError, "different worktree") || calls != 0 {
		t.Fatalf("cross-worktree recovery = %+v, translator calls %d", history, calls)
	}
}

func TestMekugiTranslationStillHonorsPatchLimit(t *testing.T) {
	prefix := "*** Begin Patch\n*** Add File: a\n"
	suffix := "\n*** End Patch\n"
	patch := prefix + strings.Repeat("x", maxMekugiPatchBytes-len(prefix)-len(suffix)+1) + suffix
	translator := mekugiTranslatorFunc(func(context.Context, string, string) ([]byte, error) {
		return []byte(patch), nil
	})
	transform, _, _, _ := newMekugiTestTransform(t, translator)
	_, err := transform.translate("call-1", testMekugiScript, nil)
	if err == nil || !strings.Contains(err.Error(), "translation exceeds") {
		t.Fatalf("oversized carrier error = %v", err)
	}
}
