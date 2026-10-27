// Package build adapts Qi selection, assembly and legacy records to the Gordis
// build engine. It does not implement module resolution, compilation or delivery.
package build

type Options struct {
	Manifest string
	Output   string
	Sources  []string
	Request  *Request
}
