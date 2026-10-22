// This fixture advertises the previous Tool wire boundary. The Host must reject
// it during Describe, before Initialize can activate its business plugin.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/RussellLuo/gordis"
	rpc "github.com/RussellLuo/gordis-rpc"
)

type legacyProvider interface {
	ListTools(context.Context) ([]legacyTool, error)
}
type legacyTool struct {
	Name          string
	TimeoutMillis int64
}

var key = gordis.NewKey[legacyProvider]("qi/tools/provider")
var binding = rpc.NewBinding(key, "rpcgen.json/1:1", rpc.Codec[legacyProvider]{
	Client: func(rpc.Invoker) legacyProvider { return new(plugin) },
	Server: func(legacyProvider) rpc.Handler {
		return rpc.Handler{Methods: map[string]rpc.Unary{"ListTools": func(context.Context, json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("legacy must not execute")
		}}}
	},
})

type plugin struct{}

func (*plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "legacy", New: func() gordis.Plugin { return new(plugin) }, Provides: []gordis.ServiceSpec{key.Spec()}}
}
func (p *plugin) Activate(_ context.Context, s *gordis.Scope) error {
	if err := os.WriteFile(os.Getenv("QI_N02_LEGACY_ACTIVATED"), []byte("activated"), 0600); err != nil {
		return err
	}
	return s.Provide(key, legacyProvider(p))
}
func (*plugin) ListTools(context.Context) ([]legacyTool, error) {
	return nil, errors.New("legacy fixture must not be called")
}
func main() {
	if err := rpc.ServeUnit(context.Background(), rpc.ServeUnitOptions{Identity: rpc.Identity{ID: "legacy", Version: "dev", Protocol: rpc.Protocol}, Unit: rpc.Unit{Plugins: []gordis.Plugin{new(plugin)}, Instances: []gordis.InstanceSpec{{ID: "legacy", Plugin: "legacy"}}, Exports: []rpc.Export{{Instance: "legacy", Binding: binding}}}}); err != nil {
		os.Exit(1)
	}
}
