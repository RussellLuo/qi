package registration

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/RussellLuo/qi/kernel/boot/internal/configcodec"
	appprofile "github.com/RussellLuo/qi/kernel/boot/internal/profile"
)

// PreparedProfile separates frozen declarations from the ID-only execution graph.
// Preparation never inspects artifacts or activates plugins.
type PreparedProfile struct {
	Source       appprofile.ResolvedProfile
	Profile      appprofile.Profile
	Declarations []appprofile.ComposedInstance
}

// Diagnose adds only references belonging to instances named by an error.
func (p PreparedProfile) Diagnose(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	for _, entry := range p.Source.Declarations {
		in := entry.Spec
		if configcodec.IsPluginPath(in.Plugin) && (strings.Contains(message, `instance "`+in.ID+`"`) || strings.Contains(message, "instance "+in.ID+":") || strings.Contains(message, "mount "+in.ID+":") || strings.Contains(message, "capability "+in.ID+":")) {
			err = fmt.Errorf("instance %q plugin %q: %w", in.ID, in.Plugin, err)
		}
	}
	return err
}

// Prepare is the single declaration-to-execution boundary, including disabled
// type admission. Every initial load or replan must pass through it.
func Prepare(registrations []PluginRegistration, source appprofile.ResolvedProfile) (PreparedProfile, error) {
	_, descriptors, err := ResolveRegistrations(registrations)
	if err != nil {
		return PreparedProfile{}, err
	}
	frozen := source.Clone()
	result := PreparedProfile{Source: frozen, Profile: source.Profile.Clone(), Declarations: source.Clone().Declarations}
	var handlers []PluginRegistration
	for _, r := range registrations {
		if r.ProfileVisible && r.ResolvePath != nil {
			handlers = append(handlers, r)
		}
	}
	adapt := func(in appprofile.InstanceSpec) (appprofile.InstanceSpec, error) {
		original := in.Plugin
		fail := func(err error) (appprofile.InstanceSpec, error) {
			return in, fmt.Errorf("instance %q plugin %q: %w", in.ID, original, err)
		}
		if err := configcodec.CheckPlugin(original, "instance "+in.ID); err != nil {
			return fail(err)
		}
		if !configcodec.IsPluginPath(original) {
			return in, nil
		}
		if len(handlers) == 0 {
			return fail(fmt.Errorf("host does not include local process plugin support"))
		}
		if len(handlers) != 1 {
			return fail(fmt.Errorf("ambiguous local process plugin support: %d path handlers", len(handlers)))
		}
		handler := handlers[0]
		if handler.Invocation != nil || handler.Completes {
			return fail(fmt.Errorf("path handler cannot own invocation or completion"))
		}
		command := original
		if !filepath.IsAbs(command) {
			if !filepath.IsAbs(source.BaseDir) {
				return fail(fmt.Errorf("relative program path requires an absolute Profile base directory"))
			}
			command = filepath.Join(source.BaseDir, command)
		}
		business := configcodec.CloneMap(in.Config)
		if business == nil {
			business = map[string]any{}
		}
		config, err := handler.ResolvePath(command, business)
		if err != nil {
			return fail(err)
		}
		in.Plugin, in.Config = handler.Plugin.Spec().ID, configcodec.CloneMap(config)
		return in, nil
	}
	byID := map[string]appprofile.InstanceSpec{}
	for i := range result.Declarations {
		in, err := adapt(result.Declarations[i].Spec)
		if err != nil {
			return PreparedProfile{}, err
		}
		result.Declarations[i].Spec = in
		byID[in.ID] = in
	}
	for i := range result.Profile.Instances {
		in, found := byID[result.Profile.Instances[i].ID]
		var err error
		if !found {
			in, err = adapt(result.Profile.Instances[i])
		}
		if err != nil {
			return PreparedProfile{}, err
		}
		result.Profile.Instances[i] = in
	}
	catalog, err := appprofile.NewCatalog(descriptors)
	if err != nil {
		return PreparedProfile{}, err
	}
	if err := catalog.ValidateDeclarations(result.Declarations); err != nil {
		return PreparedProfile{}, result.Diagnose(err)
	}
	// Programmatic graphs may have no declaration table; admit their active types too.
	active := make([]appprofile.ComposedInstance, len(result.Profile.Instances))
	for i, in := range result.Profile.Instances {
		active[i] = appprofile.ComposedInstance{Spec: in}
	}
	if err := catalog.ValidateDeclarations(active); err != nil {
		return PreparedProfile{}, result.Diagnose(err)
	}
	result.Profile = result.Profile.Clone()
	return result, nil
}

// ValidateAssembly allows prototype binding while preserving the selected types
// and all trusted admission and reference metadata.
func ValidateAssembly(selected, bound []PluginRegistration) error {
	if _, _, err := ResolveRegistrations(bound); err != nil {
		return err
	}
	if len(selected) != len(bound) {
		return fmt.Errorf("Assemble must preserve selected registrations")
	}
	byID := map[string]PluginRegistration{}
	for _, r := range bound {
		byID[r.Plugin.Spec().ID] = r
	}
	for _, a := range selected {
		b, ok := byID[a.Plugin.Spec().ID]
		if !ok {
			return fmt.Errorf("Assemble removed selected plugin %q", a.Plugin.Spec().ID)
		}
		specA, specB := a.Plugin.Spec(), b.Plugin.Spec()
		specA.New, specB.New = nil, nil
		if reflect.TypeOf(a.Plugin) != reflect.TypeOf(b.Plugin) || !reflect.DeepEqual(specA, specB) {
			return fmt.Errorf("Assemble changed selected plugin type %q", a.Plugin.Spec().ID)
		}
		// reflect.Value preserves function identity, including closure environments.
		functionsA := []any{a.ResolvePath, a.ValidateCandidate, a.Descriptor.ValidateConfig, a.Descriptor.DescribeModels}
		functionsB := []any{b.ResolvePath, b.ValidateCandidate, b.Descriptor.ValidateConfig, b.Descriptor.DescribeModels}
		for i := range functionsA {
			if reflect.ValueOf(functionsA[i]) != reflect.ValueOf(functionsB[i]) {
				return fmt.Errorf("Assemble changed trusted metadata for %q", a.Plugin.Spec().ID)
			}
		}
		id := a.Plugin.Spec().ID
		a.Plugin, b.Plugin = nil, nil
		a.ResolvePath, b.ResolvePath = nil, nil
		a.ValidateCandidate, b.ValidateCandidate = nil, nil
		a.Descriptor.ValidateConfig, b.Descriptor.ValidateConfig = nil, nil
		a.Descriptor.DescribeModels, b.Descriptor.DescribeModels = nil, nil
		if !reflect.DeepEqual(a, b) {
			return fmt.Errorf("Assemble changed trusted metadata for %q", id)
		}
	}
	return nil
}
