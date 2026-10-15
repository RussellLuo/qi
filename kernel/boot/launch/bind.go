package launch

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/RussellLuo/gordis"
)

// Bind verifies that run-local preparation cannot invalidate the dependency
// plan. New closures may differ (they carry callbacks), but type, identity and
// service Specs remain those that were preflighted. No plugin is activated.
func Bind(prepared Prepared, plugins []gordis.Plugin) ([]gordis.Plugin, error) {
	if prepared == nil {
		return append([]gordis.Plugin(nil), plugins...), nil
	}

	type definition struct {
		typ                reflect.Type
		requires, provides []gordis.ServiceSpec
	}
	before := map[string]definition{}

	for _, plugin := range plugins {
		if nilPlugin(plugin) {
			return nil, fmt.Errorf("invocation binding received a nil prototype")
		}

		spec := plugin.Spec()
		if _, exists := before[spec.ID]; exists {
			return nil, fmt.Errorf("invocation binding received duplicate type %q", spec.ID)
		}

		before[spec.ID] = definition{
			reflect.TypeOf(plugin),
			append([]gordis.ServiceSpec(nil), spec.Requires...),
			append([]gordis.ServiceSpec(nil), spec.Provides...),
		}
	}

	bound, err := prepared.Bind(append([]gordis.Plugin(nil), plugins...))
	if err != nil {
		return nil, err
	}
	if len(bound) != len(plugins) {
		return nil, fmt.Errorf("invocation binding changed registered type count")
	}

	seen := map[string]bool{}

	for _, plugin := range bound {
		if nilPlugin(plugin) {
			return nil, fmt.Errorf("invocation binding returned a nil prototype")
		}

		spec := plugin.Spec()
		previous, exists := before[spec.ID]
		if !exists ||
			seen[spec.ID] ||
			spec.New == nil ||
			reflect.TypeOf(plugin) != previous.typ ||
			!slices.Equal(spec.Requires, previous.requires) ||
			!slices.Equal(spec.Provides, previous.provides) {
			return nil, fmt.Errorf("invocation binding changed preflighted Spec %q", spec.ID)
		}

		seen[spec.ID] = true
	}

	return bound, nil
}

func nilPlugin(plugin gordis.Plugin) bool {
	return plugin == nil || reflect.ValueOf(plugin).Kind() == reflect.Pointer && reflect.ValueOf(plugin).IsNil()
}
