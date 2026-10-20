package main

import (
	"flag"
	"fmt"
	rpc "github.com/RussellLuo/gordis-rpc"
	"github.com/RussellLuo/qi/builtin/tools/process"
	"github.com/RussellLuo/qi/builtin/tools/process/testdata/fixture"
	"github.com/RussellLuo/qi/builtin/tools/process/testdata/fixture/config"
	"os"
)

func main() {
	id := flag.String("package", "", "")
	version := flag.String("version", "", "")
	pending := flag.Int("pending", 16, "")
	flag.Parse()
	declaration, err := rpc.NewConfig[config.Config]()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(process.Main(new(fixture.Plugin), process.ServerOptions{Config: declaration, ID: *id, Version: *version, Limits: process.Limits{MaxPending: *pending}}))
}
