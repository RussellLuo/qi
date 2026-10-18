package main

import (
	"context"
	"fmt"
	"os"

	toolprocess "github.com/RussellLuo/qi/builtin/tools/process"
	"github.com/RussellLuo/qi/builtin/tools/process/testdata/fixture"
)

func main() {
	var err error
	if os.Getenv("QI_PT02_PROFILE") == "1" {
		err = toolprocess.ServePlugin(context.Background(), new(fixture.ProfilePlugin))
	} else if os.Getenv("QI_PT01_OVERRIDE") == "1" {
		err = toolprocess.ServePlugin(context.Background(), new(fixture.Plugin),
			toolprocess.ServerOptions{ID: "fixture.override", Version: "v2"})
	} else {
		err = toolprocess.ServePlugin(context.Background(), new(fixture.Plugin))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
