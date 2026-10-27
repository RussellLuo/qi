package build

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyIgnoresNewEnvironment(t *testing.T) {
	t.Setenv("QI_VERSION", "invalid new query")
	options, err := parseLegacyCLI([]string{"--manifest", "old.yaml", "--output", "out", "--source", CoreModule + "=./qi"})
	if err != nil || options.Request != nil || options.Manifest != "old.yaml" {
		t.Fatal(options, err)
	}
}

func TestHelpDoesNotReadSourcesOrOutput(t *testing.T) {
	t.Setenv("QI_VERSION", "invalid query")
	path := filepath.Join(t.TempDir(), "missing")
	if code := Run(context.Background(), []string{"--with", CoreModule + "=" + path, "--help"}, io.Discard, io.Discard); code != 0 {
		t.Fatal(code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestHelpFlagValuesAndSeparator(t *testing.T) {
	for _, args := range [][]string{{"--with", "--help"}, {"--", "--help"}} {
		if code := Run(context.Background(), args, io.Discard, io.Discard); code == 0 {
			t.Fatalf("%v incorrectly requested help", args)
		}
	}
}

func TestLegacyArgumentBoundary(t *testing.T) {
	if options, err := parseLegacyCLI([]string{"-manifest=old.yaml", "-output=qi", "--"}); err != nil || options.Manifest != "old.yaml" {
		t.Fatal(options, err)
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--manifest", "old.yaml"}, "requires --output"},
		{[]string{"--manifest", "old.yaml", "--output", "qi", "--with", "example.com/x"}, "cannot be mixed"},
		{[]string{"--manifest", "old.yaml", "--output", "qi", "main"}, "cannot be mixed"},
		{[]string{"--manifest", "one", "--manifest", "two"}, "duplicate --manifest"},
		{[]string{"--manifest="}, "nonempty"},
		{[]string{"--manifest"}, "requires a value"},
		{[]string{"--source", "x=y"}, "requires legacy"},
	} {
		if _, err := parseLegacyCLI(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: %v", tc.args, err)
		}
	}
}
