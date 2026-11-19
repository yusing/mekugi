package run

import (
	"github.com/yusing/mekugi/contracts/command"
)

// Source: internal/router/toolplugin/native_builtins.go:12:12@[e702dffa] mrun description.
var Command = command.Definition[Service]{Name: "mrun", Run: Service.Execute}

// Service carries no background state; each command owns its local capture.
type Service struct{}
