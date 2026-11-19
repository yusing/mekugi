package changes

import "github.com/yusing/mekugi/contracts/command"

var Command = command.Definition[Service]{Name: "mchanges", Run: Service.Execute}
