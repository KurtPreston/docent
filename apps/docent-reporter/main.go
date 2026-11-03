package main

import (
	"fmt"
	"os"

	"github.com/KurtPreston/docent/apps/docent-reporter/internal/cli"
	"github.com/KurtPreston/docent/libs/buildinfo"
)

func main() {
	if buildinfo.IsVersionArg(os.Args[1:]) {
		fmt.Println("docent-reporter " + buildinfo.String())
		return
	}
	cli.Main()
}
