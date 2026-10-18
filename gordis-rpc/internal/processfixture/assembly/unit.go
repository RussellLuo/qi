package assembly

import (
	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis-rpc"
	"github.com/RussellLuo/gordis-rpc/internal/processfixture"
	"github.com/RussellLuo/gordis-rpc/internal/processfixture/business"
)

func Unit(mode string) rpc.Unit {
	boundary := processfixture.Boundary(mode)
	u := rpc.Unit{Plugins: []gordis.Plugin{&business.Plugin{Mode: mode}}, Instances: []gordis.InstanceSpec{{ID: "business", Plugin: "processfixture/business"}}, Imports: boundary.Imports}
	for _, binding := range boundary.Exports {
		u.Exports = append(u.Exports, rpc.Export{Instance: "business", Binding: binding})
	}
	if mode == "single" || mode == "multi" || mode == "duplex" {
		u.ConfigTarget = "business"
	}
	return u
}
