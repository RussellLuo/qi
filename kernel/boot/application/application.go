// Package application describes explicit static capabilities and pure composition.
package application

import (
	"github.com/RussellLuo/qi/kernel/boot/internal/bundles"
	privateprofile "github.com/RussellLuo/qi/kernel/boot/internal/profile"
	privateregistration "github.com/RussellLuo/qi/kernel/boot/internal/registration"
	"github.com/RussellLuo/qi/kernel/boot/profile"
	"github.com/RussellLuo/qi/kernel/boot/registration"
)

// Extension is a trusted, explicitly selected set. No default set is injected.
type Extension struct {
	Registrations []registration.PluginRegistration
	Bundles       []profile.BundleDefinition
}

// Application contains only static launch facts. Advanced assembly policies
// remain internal until their separate public adaptation has been validated.
type Application struct {
	Registrations []registration.PluginRegistration
	BundleCatalog profile.BundleCatalog
}

// Compose uses the launcher's registration and schema checks, and freezes
// detached Bundle definitions. Duplicate IDs are errors, never overrides.
func Compose(extensions ...Extension) (Application, error) {
	var app Application
	var definitions []profile.BundleDefinition

	for _, extension := range extensions {
		app.Registrations = append(app.Registrations, extension.Registrations...)
		definitions = append(definitions, extension.Bundles...)
	}

	// Validate metadata for private types too; visibility still controls Profile
	// admission when the unchanged registrations reach the launcher.
	metadata := append([]registration.PluginRegistration(nil), app.Registrations...)

	for i := range metadata {
		metadata[i].ProfileVisible = true
	}

	_, descriptors, err := privateregistration.ResolveRegistrations(metadata)
	if err != nil {
		return Application{}, err
	}
	if _, err := privateprofile.NewCatalog(descriptors); err != nil {
		return Application{}, err
	}

	// Bundle definitions are detached only after every registration passes
	// validation, so callers receive a complete static application or an error.
	catalog, err := bundles.NewCatalogFromDefinitions(definitions...)
	if err != nil {
		return Application{}, err
	}

	app.BundleCatalog = catalog

	return app, nil
}
