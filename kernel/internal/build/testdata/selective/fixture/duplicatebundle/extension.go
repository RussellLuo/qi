package duplicatebundle

import (
	applicationapi "github.com/RussellLuo/qi/kernel/boot/application"
	builtin "github.com/RussellLuo/qi/kernel/bundle"
)

func Extension() applicationapi.Extension {
	return applicationapi.Extension{Bundles: builtin.Extension().Bundles}
}
