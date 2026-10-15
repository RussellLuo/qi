package duplicate

import (
	"example.com/qi-n02-fixture/base"
	applicationapi "github.com/RussellLuo/qi/kernel/boot/application"
)

func Extension() applicationapi.Extension { return base.Extension() }
