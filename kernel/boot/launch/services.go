package launch

import (
	"github.com/RussellLuo/gordis"
)

var LaunchContextKey = gordis.NewKey[*Context]("qi/launch-context")
