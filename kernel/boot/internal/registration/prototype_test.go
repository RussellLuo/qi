package registration_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/RussellLuo/gordis"
	"github.com/RussellLuo/qi/kernel/boot/internal/registration"
	"github.com/RussellLuo/qi/kernel/boot/profile"
)

type purePrototype struct {
	Prefix    string `json:"prefix"`
	Mode      string `json:"mode"`
	activated *bool
}

func (p *purePrototype) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{ID: "test/pure", New: func() gordis.Plugin { return &purePrototype{activated: p.activated} }}
}
func (p *purePrototype) Activate(context.Context, *gordis.Scope) error {
	*p.activated = true
	return nil
}
func (p *purePrototype) Validate() error {
	if p.Mode == "validate" {
		return errors.New("business validation")
	}
	return nil
}
func TestPrototypePlanningIsPureAndValidatesConfiguration(t *testing.T) {
	activated := false
	checks := 0
	r := []registration.PluginRegistration{{Plugin: &purePrototype{activated: &activated}, ProfileVisible: true, Descriptor: profile.PluginDescriptor{
		ConfigSchema: json.RawMessage(`{"type":"object","properties":{"prefix":{"type":"string"},"mode":{"type":"string"}},"required":["prefix"],"additionalProperties":false}`),
		ValidateConfig: func(raw json.RawMessage) error {
			checks++
			var config purePrototype
			if err := json.Unmarshal(raw, &config); err != nil {
				return err
			}
			if config.Prefix == "forbidden" {
				return errors.New("pure config validation failed")
			}
			return nil
		},
	}}}
	p := profile.Profile{Instances: []profile.InstanceSpec{{ID: "pure", Plugin: "test/pure", Config: map[string]any{"prefix": "hello"}}}}
	candidate, err := registration.Plan(r, p)
	if err != nil || len(candidate.Mounts) != 1 || checks != 1 || activated {
		t.Fatal(candidate, err, checks, activated)
	}
	for _, config := range []map[string]any{{"prefix": 42}, {"prefix": "x", "unknown": true}, {"prefix": "forbidden"}, {"prefix": "x", "mode": "validate"}} {
		invalid := p.Clone()
		invalid.Instances[0].Config = config
		if _, err := registration.Plan(r, invalid); err == nil {
			t.Fatal("invalid configuration accepted", config)
		}
	}
	if activated {
		t.Fatal("preflight activated prototype")
	}
}
