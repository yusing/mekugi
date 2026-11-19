package outline

import "github.com/yusing/mekugi/contracts/command"

type Service struct{}

var Command = command.Definition[Service]{Name: "inspect_file", ObserveRead: true, Run: Service.Execute}
