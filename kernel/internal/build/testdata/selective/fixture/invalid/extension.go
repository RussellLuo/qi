package invalid

import (
	"example.com/qi-n02-fixture/optional"
	"github.com/RussellLuo/qi/kernel/boot/application"
)

func Extension() application.Extension {
	result := optional.Extension()
	result.Registrations[0].Descriptor.ConfigSchema = []byte(`{"type":"invalid-schema-type"}`)
	return result
}
