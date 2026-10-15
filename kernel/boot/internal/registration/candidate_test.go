package registration_test

import (
	"strings"
	"testing"

	"github.com/RussellLuo/qi/kernel/boot/internal/registration"
	"github.com/RussellLuo/qi/kernel/boot/profile"
)

func TestInvocationCapabilitiesRejectsMultipleCompletionOwners(t *testing.T) {
	activated := false
	registrations := []registration.PluginRegistration{{Plugin: &purePrototype{activated: &activated}, Completes: true}}
	p := profile.Profile{Instances: []profile.InstanceSpec{{ID: "first", Plugin: "test/pure"}}}
	_, _, owner, err := registration.InvocationCapabilities(registrations, p)
	if err != nil || owner != "first" {
		t.Fatal(owner, err)
	}
	p.Instances = append(p.Instances, profile.InstanceSpec{ID: "second", Plugin: "test/pure"})
	if _, _, _, err := registration.InvocationCapabilities(registrations, p); err == nil || !strings.Contains(err.Error(), "multiple completion owners") {
		t.Fatal("multiple completion owners accepted", err)
	}
	if activated {
		t.Fatal("capability selection activated a plugin")
	}
}
