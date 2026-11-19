package read

import "github.com/yusing/mekugi/contracts/command"

type Service struct{}

var Command = command.Definition[Service]{Name: "mcat", ObserveRead: true, Run: Service.Execute}
