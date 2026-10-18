package bundles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFreezesSelectedDirectoryWithoutFollowingSymlink(t *testing.T) {
	actual, selected := t.TempDir(), t.TempDir()
	target := filepath.Join(actual, "profile.yaml")
	if err := os.WriteFile(target, []byte("instances: [{id: tool, plugin: ./program}]"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(selected, "profile.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		t.Chdir(t.TempDir())
		resolved, err := LoadWith(link, nil)
		if err != nil || resolved.BaseDir != selected || resolved.Clone().BaseDir != selected {
			t.Fatal(resolved, err)
		}
	}
}
