package build

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// One real build checks the Qi entrypoint against public kernel APIs. Small
// module inputs live here; the full external acceptance matrix is not a Go test.
func TestBuildSelectedExtensions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	core, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for name, source := range map[string]string{
		"builtin/go.mod": "module github.com/RussellLuo/qi/builtin\n\ngo 1.27\nrequire github.com/RussellLuo/qi/kernel v0.0.0\n",
		"builtin/bundle/extension.go": `package bundle
import core "github.com/RussellLuo/qi/kernel/bundle"
var Extension = core.Extension
`,
		"extension/go.mod": "module example.com/extension\n\ngo 1.27\nrequire github.com/RussellLuo/qi/kernel v0.0.0\n",
		"extension/extension.go": `package extension
import (
 "github.com/RussellLuo/qi/kernel/boot/application"
 "github.com/RussellLuo/qi/kernel/boot/profile"
)
func Extension() application.Extension {
 return application.Extension{Bundles: []profile.BundleDefinition{{ID: "selected"}}}
}
`,
		"extension/extra/extension.go": `package extra
import (
 "github.com/RussellLuo/qi/kernel/boot/application"
 "github.com/RussellLuo/qi/kernel/boot/profile"
)
func Extension() application.Extension {
 return application.Extension{Bundles: []profile.BundleDefinition{{ID: "extra"}}}
}
`,
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(root, "qi")
	t.Setenv("QI_VERSION", "")
	var out, diagnostic bytes.Buffer
	code := Run(ctx, []string{
		"--replace", CoreModule + "=" + core,
		"--replace", BuiltinModule + "=" + filepath.Join(root, "builtin"),
		"--with", "example.com/extension=" + filepath.Join(root, "extension"),
		"--with", "example.com/extension/extra=" + filepath.Join(root, "extension"),
		"--output", binary,
	}, &out, &diagnostic)
	if code != 0 {
		t.Fatal(code, out.String(), diagnostic.String())
	}
	answer, err := exec.CommandContext(ctx, binary, "--help").CombinedOutput()
	if err != nil || !strings.Contains(string(answer), "Usage:") {
		t.Fatal(err, string(answer))
	}
	// Profile lookup proves both selected factories reached Compose. Its pure
	// help path needs no model, credentials, or runtime task implementation.
	profile := filepath.Join(root, "profile.yaml")
	if err := os.WriteFile(profile, []byte("bundles: [selected, extra]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	answer, err = exec.CommandContext(ctx, binary, "--profile", profile, "--", "--help").CombinedOutput()
	if err != nil || !strings.Contains(string(answer), "This Profile does not accept application arguments") {
		t.Fatal(err, string(answer))
	}
}
