package main

import (
	"context"
	"fmt"
	"os"

	"github.com/RussellLuo/gordis/rpc"
	toolprocess "github.com/RussellLuo/qi/builtin/tools/process"
	"github.com/RussellLuo/qi/builtin/tools/process/testdata/fixture"
	"github.com/RussellLuo/qi/builtin/tools/process/testdata/fixture/config"
)

func main() {
	declaration, err := rpc.NewConfig[config.Config]()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if os.Getenv("QI_C03_NO_CONFIG") == "1" {
		declaration = rpc.ConfigSpec{}
	}
	if os.Getenv("QI_PT02_PROFILE") == "1" {
		err = toolprocess.ServePlugin(context.Background(), new(fixture.ProfilePlugin), toolprocess.ServerOptions{Config: declaration})
	} else if os.Getenv("QI_PT01_OVERRIDE") == "1" {
		err = toolprocess.ServePlugin(context.Background(), new(fixture.Plugin),
			toolprocess.ServerOptions{ID: "fixture.override", Version: "v2", Config: declaration})
	} else {
		err = toolprocess.ServePlugin(context.Background(), new(fixture.Plugin), toolprocess.ServerOptions{Config: declaration})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
