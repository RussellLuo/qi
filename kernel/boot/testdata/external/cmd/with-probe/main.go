package main

import (
	"example.com/qi-boot-fixture/probe"
	"github.com/RussellLuo/qi/kernel/boot"
	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/launch"
	"os"
)

func main() {
	app, err := application.Compose(probe.Extension())
	if err != nil {
		panic(err)
	}
	os.Exit(boot.Run(os.Args[1:], launch.Input{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}, app))
}
