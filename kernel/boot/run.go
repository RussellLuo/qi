// Package boot provides the public process entry for explicitly assembled programs.
package boot

import (
	applicationapi "github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/internal/cli"
	launchapi "github.com/RussellLuo/qi/kernel/boot/launch"
)

// Run uses the same Profile launcher, signals and stream handling as cmd/qi.
func Run(argv []string, input launchapi.Input, application applicationapi.Application) int {
	return cli.Run(argv, input, cli.Application{
		Registrations: application.Registrations, BundleCatalog: application.BundleCatalog,
	})
}
