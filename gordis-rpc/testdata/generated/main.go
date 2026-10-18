package main

import (
	"context"
	"fmt"
	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis-rpc"
	"github.com/RussellLuo/gordis-rpc/internal/genfixture/bindings"
	"github.com/RussellLuo/gordis-rpc/internal/genfixture/business"
	"os"
)

func main() {
	u := rpc.Unit{Plugins: []gordis.Plugin{&business.Plugin{}}, Instances: []gordis.InstanceSpec{{ID: "business", Plugin: "genfixture/business"}}, ConfigTarget: "business", Exports: []rpc.Export{{Instance: "business", Binding: bindings.Service}}}
	if err := rpc.ServeUnit(context.Background(), rpc.ServeUnitOptions{Identity: rpc.Identity{ID: "genfixture/program", Version: "1", Protocol: rpc.Protocol}, Unit: u}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
