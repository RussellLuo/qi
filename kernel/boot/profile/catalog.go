package profile

// BundleCatalog exposes frozen definitions without a dependency on the product
// catalog implementation. Lookup must return a definition and its content digest.
type BundleCatalog interface {
	Lookup(id string) (BundleDefinition, string, bool)
}
