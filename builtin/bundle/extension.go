// Package bundle describes the default types and the base Bundle.
package bundle

import (
	"github.com/RussellLuo/qi/builtin/cli/headless"
	llmopenai "github.com/RussellLuo/qi/builtin/llm/openai/plugin"
	sessionjsonl "github.com/RussellLuo/qi/builtin/session/jsonl/plugin"
	fsplugin "github.com/RussellLuo/qi/builtin/tools/fs/plugin"
	toolprocess "github.com/RussellLuo/qi/builtin/tools/process"
	"github.com/RussellLuo/qi/kernel/boot/application"
	corebundle "github.com/RussellLuo/qi/kernel/bundle"
)

// Extension adds the original concrete providers to the core declarations.
// Profiles select instances; this factory starts no runtime services.
func Extension() application.Extension {
	extension := corebundle.Extension()
	for _, capability := range []application.Extension{
		sessionjsonl.Extension(), headless.Extension(), llmopenai.Extension(), fsplugin.Extension(), toolprocess.Extension(),
	} {
		extension.Registrations = append(extension.Registrations, capability.Registrations...)
	}
	return extension
}
