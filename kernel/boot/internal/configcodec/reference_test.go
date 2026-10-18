package configcodec

import (
	"path/filepath"
	"testing"
)

func TestPluginReferenceSyntax(t *testing.T) {
	for _, value := range []string{"./program", "../program", "./a b", filepath.Join(t.TempDir(), "program")} {
		if !IsPluginPath(value) || CheckPlugin(value, "test") != nil || ValidPluginID(value) {
			t.Fatal(value)
		}
	}
	for _, value := range []string{"example/tool-process", "program", "file.exe"} {
		if IsPluginPath(value) || CheckPlugin(value, "test") != nil {
			t.Fatal(value)
		}
	}
	for _, value := range []string{"", "./bad\x00path", "~/program", "$PROGRAM", "program arg"} {
		if CheckPlugin(value, "test") == nil {
			t.Fatal(value)
		}
	}
}
