package tools

import "github.com/RussellLuo/gordis"

// RuntimeKey identifies the shared Tool Runtime service.
var RuntimeKey = gordis.NewKey[*Runtime]("qi/tools")
