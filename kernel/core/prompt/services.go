package prompt

import "github.com/RussellLuo/gordis"

// RuntimeKey is the service provided by this capability's Runtime provider.
var RuntimeKey = gordis.NewKey[*Runtime]("qi/prompt")
