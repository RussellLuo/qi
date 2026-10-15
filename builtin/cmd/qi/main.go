package main

import (
	applicationapi "github.com/RussellLuo/qi/kernel/boot/application"
	launchapi "github.com/RussellLuo/qi/kernel/boot/launch"
	"os"

	builtin "github.com/RussellLuo/qi/builtin/bundle"
	"github.com/RussellLuo/qi/kernel/boot"
)

func main() {
	app, err := applicationapi.Compose(builtin.Extension())
	if err != nil {
		panic(err)
	}

	os.Exit(boot.Run(os.Args[1:], launchapi.Input{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}, app))
}
