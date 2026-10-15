package main

import (
	"context"
	"fmt"
	"os"

	greet "github.com/RussellLuo/qi/builtin/examples/tool-process"
	toolprocess "github.com/RussellLuo/qi/builtin/tools/process"
)

func main() {
	if err := toolprocess.ServePlugin(context.Background(), os.Stdin, os.Stdout, new(greet.Plugin)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
