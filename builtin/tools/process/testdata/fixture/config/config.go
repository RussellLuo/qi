// Package config declares the fixture program's public configuration separately
// from its ordinary business Plugin.
package config

import (
	"context"
	"errors"

	toolprocess "github.com/RussellLuo/qi/builtin/tools/process"
	"github.com/RussellLuo/qi/builtin/tools/process/testdata/fixture"
)

type Config struct {
	Mode      string `json:"mode,omitempty"`
	Directory string `json:"directory,omitempty"`
	Prefix    string `json:"prefix,omitempty" jsonschemax:"maxLength=100"`
}

func (Config) New() Config { return Config{Prefix: "Hello, "} }

func (c Config) Configure(context.Context) (map[string]any, error) {
	if c.Mode == "configure-error" {
		return nil, errors.New("fixture configuration failed")
	}
	return toolprocess.ConfigForTool(fixture.Plugin{
		Mode: c.Mode, Directory: c.Directory, Prefix: c.Prefix,
	}), nil
}
