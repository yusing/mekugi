package query

import "github.com/yusing/mekugi/contracts/command"

type Service struct{}

var Command = command.Definition[Service]{Name: "msymbol", ObserveRead: true, Run: Service.Execute}
