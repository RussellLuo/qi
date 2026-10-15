package launch

import (
	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/core/protocol"
)

// HeaderCheckBinder binds admission before Runtime recovery. Implementations
// return a detached prototype and run check before any prior recovery hook.
type HeaderCheckBinder interface {
	WithHeaderCheck(check func(protocol.SessionHeader) error) gordis.Plugin
}
