package codexinstructions

import (
	_ "embed"
	"strings"
	"text/template"
)

//go:embed mekugi-recovery.tmpl
var recoverySource string

var recoveryTemplate = template.Must(template.New("mekugi-recovery").Parse(recoverySource))

// RecoveryGuidance renders dynamic rejected-script guidance.
func RecoveryGuidance(references string) string {
	var rendered strings.Builder
	if err := recoveryTemplate.Execute(&rendered, struct{ References string }{references}); err != nil {
		panic(err)
	}
	return rendered.String()
}
