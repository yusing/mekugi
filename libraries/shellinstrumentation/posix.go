package shellinstrumentation

// Source: internal/execsegment/sh.go:1:123@[465a8ff3]

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"

	"github.com/yusing/mekugi/libraries/commandargs"

	"mvdan.cc/sh/v3/syntax"
)

const ShTrackerEnvironment = "MEKUGI_SH_TRACKER"

const shPrefix = ":; __mekugi_b() { return $?; }; __mekugi_e() { return $?; }; [ ! -r "

var shBoundary = regexp.MustCompile(`\{ __mekugi_b [0-9]+ && :; |; __mekugi_e [0-9]+ && :; \}`)

// ShScript inserts execution boundaries before native shell setup and guard
// rewriting. The digest identifies their authored source without copying it into argv.
func ShScript(tracker, script string) string { return shScript(tracker, script, false) }

func shScript(tracker, script string, legacy bool) string {
	program, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(script), "")
	if err != nil {
		return script
	}
	// Dash supports only fixed descriptor operands. Leave scripts that use
	// the transport's descriptors untouched, including computed duplicates.
	available := true
	syntax.Walk(program, func(node syntax.Node) bool {
		if redirect, ok := node.(*syntax.Redirect); ok {
			if redirect.N != nil && (redirect.N.Value == "7" || redirect.N.Value == "8") {
				available = false
			}
			if redirect.Op == syntax.DplIn || redirect.Op == syntax.DplOut {
				word := redirect.Word.Lit()
				if word == "" || word == "7" || word == "8" {
					available = false
				}
			}
		}
		return available
	})
	if !available {
		return script
	}
	segments, ok := Split(script)
	if !ok {
		return script
	}
	prefix := fmt.Sprintf("%s%s ] || { __mekugi_digest=%x __mekugi_count=%d; . %s; unset __mekugi_digest __mekugi_count; }; ", shPrefix, commandargs.Quote(tracker), sha256.Sum256([]byte(script)), len(segments), commandargs.Quote(tracker))
	if legacy {
		prefix = shPrefix + commandargs.Quote(tracker) + " ] || . " + commandargs.Quote(tracker) + "; "
	}
	if len(segments) > 1 {
		script = Rewrite(script, segments)
	}
	return prefix + script
}

// ShOriginal recognizes only an exact envelope produced by ShScript. Removing
// its inline boundaries keeps authored newlines and shell error line numbers.
func ShOriginal(script string) string {
	if !strings.HasPrefix(script, shPrefix) {
		return script
	}
	program, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil || len(program.Stmts) < 5 {
		return script
	}
	end := int(stmtEnd(program.Stmts[3]).Offset())
	if end+2 > len(script) || script[end:end+2] != "; " {
		return script
	}
	prefix := script[:end+2]
	// Read the quoted path by word span. Delimiters may occur in the path
	// itself, and neither replay nor display may evaluate it.
	condition, ok := program.Stmts[3].Cmd.(*syntax.BinaryCmd)
	if !ok {
		return script
	}
	test, ok := condition.X.Cmd.(*syntax.CallExpr)
	if !ok || len(test.Args) != 5 {
		return script
	}
	word := test.Args[3]
	path := script[word.Pos().Offset():word.End().Offset()]
	if len(path) < 2 || path[0] != '\'' || path[len(path)-1] != '\'' {
		return script
	}
	tracker := strings.ReplaceAll(path[1:len(path)-1], "'\\''", "'")
	original := shBoundary.ReplaceAllString(script[end+2:], "")
	// Historical envelopes did not include the source digest.
	if shScript(tracker, original, prefix == shPrefix+path+" ] || . "+path+"; ") != script {
		return script
	}
	return original
}

// ShTracker opens the control and ack FIFOs from the executing shell. The
// already-emitted boundaries run even when native setup changes the shell argv.
func ShTracker(helper, channel, directory string) string {
	return `if [ -z "${` + Guard + `+x}" ] && [ ! -e "/dev/fd/3" ] && [ ! -e "/dev/fd/4" ] && [ ! -e "/dev/fd/7" ] && [ ! -e "/dev/fd/8" ]; then
  __mekugi_d=$(` + commandargs.Quote(helper) + ` --sh-open ` + commandargs.Quote(channel) + ` ` + commandargs.Quote(directory) + ` "${BASH_VERSION-}" 2>/dev/null)
  if [ -n "$__mekugi_d" ]; then
    export ` + Guard + `=1
    if { command exec 7<>"$__mekugi_d/control" 8<>"$__mekugi_d/ack"; } 2>/dev/null; then
      (trap 'printf "decline\n"' EXIT; command exec ` + commandargs.Quote(helper) + ` --sh-run ` + commandargs.Quote(channel) + ` ` + commandargs.Quote(directory) + ` "$__mekugi_d" "$__mekugi_digest" "$__mekugi_count" 0<"$__mekugi_d/control") 3>&1 4>&2 1>&8 7>&- 8>&- 2>/dev/null &
      IFS=' ' read -r __mekugi_m __mekugi_w <&8
      case $__mekugi_m in
      relay)
        if { command exec 3>"$__mekugi_d/out" 4>"$__mekugi_d/err"; } 2>/dev/null && [ -p "/dev/fd/3" ] && [ -p "/dev/fd/4" ]; then
          exec 1>&3 2>&4 3>&- 4>&-
        else
          exec 3>&- 4>&- 7>&- 8>&-
          __mekugi_m=
        fi ;;
      esac
      case $__mekugi_m in
      observe|relay|status)
        if [ -n "${BASH_VERSION-}" ]; then
` + mktempHook(helper, ">&7", true) + `
        else
` + mktempHook(helper, ">&7", false) + `
        fi
        printf 'o\n' >&7
        __mekugi_b() { local __mekugi_s=$? __mekugi_r; printf 'b %s\n' "$1" >&7; IFS= read -r __mekugi_r <&8; return "$__mekugi_s"; }
        __mekugi_e() { local __mekugi_s=$? __mekugi_r; printf 'e %s %s\n' "$1" "$__mekugi_s" >&7; IFS= read -r __mekugi_r <&8; return "$__mekugi_s"; }
        __mekugi_f() { local __mekugi_s=$? __mekugi_r; printf 'd %s\n' "$__mekugi_s" >&7; IFS= read -r __mekugi_r <&8; }
        trap __mekugi_f EXIT
        if [ "$__mekugi_m" = observe ]; then __mekugi_b 0; fi
        ;;
      *) exec 7>&- 8>&- ;;
      esac
    fi
  fi
fi
`
}
