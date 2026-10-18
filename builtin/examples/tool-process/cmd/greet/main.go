package main

import (
	"os"

	greet "github.com/RussellLuo/qi/builtin/examples/tool-process"
	toolprocess "github.com/RussellLuo/qi/builtin/tools/process"
)

func main() { os.Exit(toolprocess.Main(new(greet.Plugin))) }
