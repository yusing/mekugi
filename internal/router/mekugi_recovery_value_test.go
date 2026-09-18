package router

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/hpatchsyntax"
	"github.com/yusing/mekugi/internal/patchtest"
)

func TestRecoveryCommandValues(t *testing.T) {
	for _, command := range []string{
		"type \"old\" \"bad\"\n", "add EOF \"bad\"\n", "type \"bad\"\n",
		"type 1:abcd <<END\nbad\nEND\n",
	} {
		baseline := "in f.txt\n" + command + "new untouched.txt\ntype \"keep\"\n"
		handle := recoveryCommands(baseline, testRecoveryHandles(baseline))[1].handle
		for _, value := range []string{`"good\n"`, "<<END\ngood\nEND", "<<'END' \ngood\nEND", "<<-END\t\n\tgood\n\tEND"} {
			result, err := recoverScriptDetailed(t.Context(), baseline, handle+" value "+value, testRecoveryHandles(baseline))
			if err != nil {
				t.Fatal(err)
			}
			parts := recoveryCommands(result.script, testRecoveryHandles(result.script))
			if parts[1].parts.value != "good\n" || parts[1].parts.target != recoveryCommands(baseline, testRecoveryHandles(baseline))[1].parts.target ||
				!strings.HasSuffix(result.script, "new untouched.txt\ntype \"keep\"\n") {
				t.Fatalf("unexpected reconstructed script: %q", result.script)
			}
		}
		for _, payload := range []string{
			handle + ` value "bad"` + "\n" + handle + ` target "different"`,
			handle + " value <<PATCH\nunterminated",
			handle + ` value "good" trailing`,
			handle + " value <<END\nbody\nWRONG",
		} {
			if _, err := recoverScriptDetailed(t.Context(), baseline, payload, testRecoveryHandles(baseline)); err == nil {
				t.Fatalf("accepted invalid correction %q", payload)
			}
		}
	}
}

func TestRecoveryCommandValueTranslation(t *testing.T) {
	transform, _, _, _ := newMekugiTestTransform(t, newInProcessMekugiTranslator(t.TempDir()))
	base := "new sample.go\ntype \"package p\\nvar =\\n\"\nnew keep.txt\ntype \"kept\"\n"
	first, err := transform.translate("original-value", base, nil)
	if err != nil || !first.EvaluatorRejected {
		t.Fatalf("initial rejection: %v, %+v", err, first)
	}
	handle := recoveryCommands(base, first.RecoveryHandles)[1].handle
	if !strings.Contains(first.TranslationError, handle+" value VALUE") {
		t.Fatal("missing command-scoped diagnostic")
	}
	payload := handle + " value <<END\npackage p\nvar X = 1\nEND"
	fixed, err := transform.translateRecovery("corrected-value", payload, nil)
	if err != nil || fixed.TranslationError != "" || fixed.Script != payload || fixed.CorrelationID != first.CorrelationID {
		t.Fatalf("value translation: %v, %+v", err, fixed)
	}
	tree, err := patchtest.Apply(map[string]string{}, fixed.Patch)
	if err != nil || tree["sample.go"] != "package p\n\nvar X = 1\n" || tree["keep.txt"] != "kept\n" {
		t.Fatalf("patch: %v, %#v", err, tree)
	}
	if _, err := recoverScriptDetailed(t.Context(), base, handle+` value "package p\nvar =\n"`, first.RecoveryHandles); err == nil {
		t.Fatal("accepted unchanged decoded value")
	}
	// Target and value corrections share one atomic immutable baseline.
	baseline := "in f.txt\ntype \"old\" \"bad\"\nadd EOF \"tail\"\n"
	commands := recoveryCommands(baseline, testRecoveryHandles(baseline))
	result, err := recoverScriptDetailed(t.Context(), baseline,
		commands[1].handle+` target "new"`+"\n"+commands[2].handle+` value "end"`, testRecoveryHandles(baseline))
	if err != nil || !strings.Contains(result.script, `type "new" "bad"`) || !strings.Contains(result.script, `add EOF "end"`) {
		t.Fatalf("combined corrections: %v, %q", err, result.script)
	}
}

func TestRecoveryCommandValueJSONEscapesReachConsumer(t *testing.T) {
	for _, encoded := range []string{`"\u001b[31m"`, `"\u0007"`, `"\u000b"`, `"\u0000"`, `"\b\f\r\t"`} {
		t.Run(encoded, func(t *testing.T) {
			transform, _, _, _ := newMekugiTestTransform(t, newInProcessMekugiTranslator(t.TempDir()))
			base := "new sample.go\ntype \"package p\\nvar =\\n\"\nnew control.txt\ntype \"old\"\n"
			first, err := transform.translate("control-original", base, nil)
			if err != nil || !first.EvaluatorRejected {
				t.Fatalf("reject: %v", err)
			}
			commands := recoveryCommands(base, first.RecoveryHandles)
			fixed, err := transform.translateRecovery("control-fixed",
				commands[1].handle+` value "package p\n"`+"\n"+commands[3].handle+" value "+encoded, nil)
			if err != nil || fixed.TranslationError != "" {
				t.Fatalf("translate: %v, %s", err, fixed.TranslationError)
			}
			want, _, err := hpatchsyntax.DecodeQuoted(encoded)
			if err != nil {
				t.Fatal(err)
			}
			parts := recoveryCommands(fixed.Evaluated, testRecoveryHandles(fixed.Evaluated))
			if parts[3].parts.value != want {
				t.Fatalf("decoded bytes %q, want %q", parts[3].parts.value, want)
			}
			// Consuming the translated patch must preserve the same control bytes.
			tree, err := patchtest.Apply(map[string]string{}, fixed.Patch)
			if err != nil || tree["control.txt"] != strings.ReplaceAll(want, "\r", "\n")+"\n" {
				t.Fatalf("consumer: %v, %q", err, tree["control.txt"])
			}
		})
	}
}
