// Command sessionapi-schema writes or checks the generated browser wire types.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionapi"
)

func main() {
	check := flag.Bool("check", false, "check generated types without modifying files")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "sessionapi-schema accepts only -check")
		os.Exit(2)
	}
	const target = "apps/mekugi/internal/sessionapi/protocol.ts"
	data, err := sessionapi.TypeScript()
	if err == nil && *check {
		var current []byte
		current, err = os.ReadFile(target)
		if err == nil && !bytes.Equal(current, data) {
			err = fmt.Errorf("%s is stale; run make generate", target)
		}
	} else if err == nil {
		err = os.WriteFile(target, data, 0644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
