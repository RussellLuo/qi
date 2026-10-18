package main

import (
	"flag"
	"github.com/RussellLuo/qi/builtin/tools/process"
	"github.com/RussellLuo/qi/builtin/tools/process/testdata/fixture"
	"os"
)

func main() {
	id := flag.String("package", "", "")
	version := flag.String("version", "", "")
	pending := flag.Int("pending", 16, "")
	flag.Parse()
	os.Exit(process.Main(new(fixture.Plugin), process.ServerOptions{ID: *id, Version: *version, Limits: process.Limits{MaxPending: *pending}}))
}
