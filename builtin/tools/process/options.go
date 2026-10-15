package process

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/RussellLuo/gordis"
	gordisprocess "github.com/RussellLuo/gordis/process"
	"github.com/RussellLuo/qi/kernel/core/tools"
)

type Options struct {
	// Command is absolute or relative to the qi executable's directory.
	Command string
	Args    []string
	// Env contains the entire child environment; the default inherits nothing.
	Env            []string
	PackageID      string
	Version        string
	Limits         gordisprocess.Limits
	StartupTimeout time.Duration
	ExitGrace      time.Duration
}

type ServerOptions struct {
	PackageID string
	Version   string
	Limits    gordisprocess.Limits
}

func defaultLimits(l gordisprocess.Limits) gordisprocess.Limits {
	if l.MaxMessage <= 0 {
		l.MaxMessage = 256 * 1024
	}
	if l.MaxPending <= 0 {
		l.MaxPending = 16
	}
	if l.Queue <= 0 {
		l.Queue = 16
	}
	return l
}

func (o Options) defaults() Options {
	o.Args = append([]string(nil), o.Args...)
	o.Env = append([]string{}, o.Env...)
	o.Limits = defaultLimits(o.Limits)
	if o.StartupTimeout <= 0 {
		o.StartupTimeout = 3 * time.Second
	}
	if o.ExitGrace <= 0 {
		o.ExitGrace = time.Second
	}
	return o
}

func pluginIdentity(id, packageID, version string) (gordisprocess.Identity, error) {
	if packageID == "" {
		packageID = fmt.Sprintf("qi.tool-plugin.%x", sha256.Sum256([]byte(id)))
	}
	if version == "" {
		version = "dev"
	}
	identity := gordisprocess.Identity{
		Protocol: gordisprocess.Protocol, Package: packageID, Version: version,
		Contracts: []string{ContractID, gordisprocess.LifecycleContract},
	}
	if !identifierPattern.MatchString(packageID) || !versionPattern.MatchString(version) {
		return gordisprocess.Identity{}, errors.New("toolprocess: invalid process identity")
	}
	return identity, nil
}

func inspectPlugin(prototype gordis.Plugin) (spec gordis.PluginSpec, err error) {
	defer func() {
		if recover() != nil {
			spec = gordis.PluginSpec{}
			err = errors.New("toolprocess: plugin Spec panicked")
		}
	}()
	if prototype == nil || (reflect.ValueOf(prototype).Kind() == reflect.Pointer && reflect.ValueOf(prototype).IsNil()) {
		return spec, errors.New("toolprocess: nil plugin prototype")
	}
	spec = prototype.Spec()
	if spec.ID == "" || spec.New == nil {
		return spec, errors.New("toolprocess: plugin requires ID and New")
	}
	if _, ok := prototype.(interface {
		Activate(context.Context, *gordis.Scope) error
	}); !ok {
		return spec, errors.New("toolprocess: plugin requires Activate")
	}
	if len(spec.Requires) != 1 || spec.Requires[0] != tools.RuntimeKey.Spec() || len(spec.Provides) != 0 {
		return spec, errors.New("toolprocess: plugin must require only Tool Runtime and provide no services")
	}
	spec.Requires = append([]gordis.ServiceSpec(nil), spec.Requires...)
	spec.Provides = append([]gordis.ServiceSpec(nil), spec.Provides...)
	return spec, nil
}
