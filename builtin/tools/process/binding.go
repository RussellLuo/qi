package process

import (
	"context"

	"github.com/RussellLuo/gordis/rpc"
	"github.com/RussellLuo/qi/builtin/tools/process/internal/bindings"
	"github.com/RussellLuo/qi/builtin/tools/process/internal/contract"
)

type Provider = contract.Provider

var ProviderKey = contract.Key

var ProviderBinding = func() rpc.Binding {
	codec := bindings.ProviderCodec()
	codec.Validate = func(ctx context.Context, service Provider) error {
		_, err := service.ListTools(ctx)
		return err
	}
	return rpc.NewBinding(ProviderKey, bindings.Provider.Version(), codec)
}()
