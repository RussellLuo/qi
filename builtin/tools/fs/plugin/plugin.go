// Package plugin binds the file tools to one Gordis generation.
package plugin

import (
	"context"
	"errors"
	"fmt"

	"github.com/RussellLuo/gordis"
	fstool "github.com/RussellLuo/qi/builtin/tools/fs"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

const PluginID = "qi/tools/fs"

type Plugin struct {
	ReadLimit         *int `json:"read_limit"`
	ReadMaxLineLength *int `json:"read_max_line_length"`
	ReadMaxBytes      *int `json:"read_max_bytes"`

	files *fstool.FS
}

func (*Plugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:       PluginID,
		Requires: []gordis.ServiceSpec{tools.RuntimeKey.Spec()},
		New:      func() gordis.Plugin { return new(Plugin) },
	}
}

func (p *Plugin) options() (fstool.Options, error) {
	options := fstool.DefaultOptions("")
	for _, limit := range []struct {
		name   string
		value  *int
		target *int
	}{
		{"read_limit", p.ReadLimit, &options.ReadLimit},
		{"read_max_line_length", p.ReadMaxLineLength, &options.ReadMaxLineLength},
		{"read_max_bytes", p.ReadMaxBytes, &options.ReadMaxBytes},
	} {
		if limit.value != nil {
			if *limit.value <= 0 {
				return options, fmt.Errorf("%s must be a positive integer", limit.name)
			}
			*limit.target = *limit.value
		}
	}
	return options, nil
}

func (p *Plugin) Validate() error {
	_, err := p.options()
	return err
}

func (p *Plugin) Activate(_ context.Context, scope *gordis.Scope) error {
	options, err := p.options()
	if err != nil {
		return err
	}
	files, err := fstool.New(options)
	if err != nil {
		return err
	}
	p.files = files
	if err := scope.Defer("close local file tools", func(context.Context) error { return files.Close() }); err != nil {
		return errors.Join(err, files.Close())
	}
	runtime, err := scope.Get(tools.RuntimeKey)
	if err != nil {
		return err
	}
	_, err = runtime.Bind(scope).RegisterBatch([]tools.Definition{
		fstool.NewReadDefinition(files), fstool.NewWriteDefinition(files),
	})
	return err
}
