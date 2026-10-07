package execsegment

import (
	"regexp"
	"strings"

	"github.com/yusing/mekugi/internal/shellsyntax"
	"mvdan.cc/sh/v3/syntax"
)

const ShTrackerEnvironment = "MEKUGI_SH_TRACKER"

const shPrefix = ":; __mekugi_b() { return $?; }; __mekugi_e() { return $?; }; [ ! -r "

var shBoundary = regexp.MustCompile(`\{ __mekugi_b [0-9]+ && :; |; __mekugi_e [0-9]+ && :; \}`)

// ShScript adds a portable startup call and the existing segment rewrite.
// Bash's startup hook recognizes this envelope before its DEBUG trap replaces
// the script. Other shells retain their own execution and no-op boundaries.
func ShScript(tracker, script string) string {
	program, err := syntax.NewParser(syntax.Variant(syntax.LangPOSIX)).Parse(strings.NewReader(script), "")
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
	prefix := shPrefix + shellsyntax.Quote(tracker) + " ] || . " + shellsyntax.Quote(tracker) + "; "
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
	// Quote is reversible without executing source. The path is between the
	// fixed test prefix and its closing bracket.
	path, _, ok := strings.Cut(strings.TrimPrefix(prefix, shPrefix), " ] || . ")
	if !ok {
		return script
	}
	if len(path) < 2 || path[0] != '\'' || path[len(path)-1] != '\'' {
		return script
	}
	tracker := strings.ReplaceAll(path[1:len(path)-1], "'\\''", "'")
	original := shBoundary.ReplaceAllString(script[end+2:], "")
	if ShScript(tracker, original) != script {
		return script
	}
	return original
}

// ShTracker runs in dash itself. Only transport differs from Bash: dash has
// no coprocess, so the existing router also supplies control and ack FIFOs.
func ShTracker(helper, channel, directory string) string {
	return `if [ -z "${` + Guard + `+x}" ] && [ ! -e "/proc/$$/fd/3" ] && [ ! -e "/proc/$$/fd/4" ] && [ ! -e "/proc/$$/fd/7" ] && [ ! -e "/proc/$$/fd/8" ]; then
  __mekugi_d=$(` + shellsyntax.Quote(helper) + ` --sh-open ` + shellsyntax.Quote(channel) + ` ` + shellsyntax.Quote(directory) + ` 2>/dev/null)
  if [ -n "$__mekugi_d" ]; then
    export ` + Guard + `=1
    if { command exec 7<>"$__mekugi_d/control" 8<>"$__mekugi_d/ack"; } 2>/dev/null; then
      (trap 'printf "decline\n"' EXIT; command exec ` + shellsyntax.Quote(helper) + ` --sh-run ` + shellsyntax.Quote(channel) + ` ` + shellsyntax.Quote(directory) + ` "$__mekugi_d" 0<"$__mekugi_d/control") 3>&1 4>&2 1>&8 7>&- 8>&- 2>/dev/null &
      IFS=' ' read -r __mekugi_m __mekugi_w <&8
      case $__mekugi_m in
      relay)
        if { command exec 3>"$__mekugi_d/out" 4>"$__mekugi_d/err"; } 2>/dev/null && [ -p "/proc/$$/fd/3" ] && [ -p "/proc/$$/fd/4" ]; then
          exec 1>&3 2>&4 3>&- 4>&-
        else
          exec 3>&- 4>&- 7>&- 8>&-
          __mekugi_m=
        fi ;;
      esac
      case $__mekugi_m in
      observe|relay|status)
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
