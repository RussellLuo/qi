package plugin

import (
	"reflect"
	"testing"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/gordis/events"
	"github.com/RussellLuo/qi/kernel/core/session"
)

func TestRuntimeSpecKeepsPersistenceOptional(t *testing.T) {
	p := new(Plugin)
	spec := p.Spec()
	if !reflect.DeepEqual(spec.Requires, []gordis.ServiceSpec{events.BusKey.Spec()}) ||
		!reflect.DeepEqual(spec.Provides, []gordis.ServiceSpec{session.StoreKey.Spec()}) ||
		!reflect.DeepEqual(spec.Requires, spec.New().Spec().Requires) {
		t.Fatalf("runtime declaration: %+v", spec)
	}
}
