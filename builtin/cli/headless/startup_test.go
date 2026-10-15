package headless

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestStartupParsing(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		tty       bool
		want      startup
		wantError string
		errorJSON bool
	}{
		{name: "words", args: []string{"run", "the", "tests"}, want: startup{Task: "run the tests"}},
		{name: "quoted", args: []string{"hello  world\nagain"}, want: startup{Task: "hello  world\nagain"}},
		{name: "interleave", args: []string{"one", "--session-id", " exact ", "two", "--json"}, want: startup{Task: "one two", SessionID: " exact ", Resume: true, JSON: true}},
		{name: "equals", args: []string{"--session-id= id ", "again"}, want: startup{Task: "again", SessionID: " id ", Resume: true}},
		{name: "repeat", args: []string{"--session-id", "a", "--session-id=b", "--json", "--json", "hello"}, want: startup{Task: "hello", SessionID: "b", Resume: true, JSON: true}},
		{name: "stdin", args: []string{}, want: startup{ReadStdin: true}},
		{name: "marker", args: []string{"-"}, tty: true, want: startup{Task: "-", ReadStdin: true}},
		{name: "dashmixed", args: []string{"-", "x"}, wantError: "`-` must be the only task argument"},
		{name: "separator", args: []string{"--", "--json"}, want: startup{Task: "--json"}},
		{name: "empty", args: []string{"   "}, wantError: taskRequired},
		{name: "tty", args: []string{}, tty: true, wantError: taskRequired},
		{name: "blank-id", args: []string{"--session-id= ", "hello"}, wantError: "--session-id requires a non-empty session id"},
		{name: "missing-id", args: []string{"--session-id"}, wantError: "option '--session-id <id>' argument missing"},
		{name: "value-json", args: []string{"--session-id", "--json"}, tty: true, wantError: taskRequired},
		{name: "value-help", args: []string{"--session-id", "--help", "hello"}, want: startup{Task: "hello", SessionID: "--help", Resume: true}},
		{name: "invalid-json", args: []string{"--json=false", "hello"}, wantError: "unknown option '--json=false'"},
		{name: "unknown-before-json", args: []string{"--bogus", "--json", "hello"}, wantError: "unknown option '--bogus'", errorJSON: true},
		{name: "unknown-after-json", args: []string{"--json", "--bogus"}, wantError: "unknown option '--bogus'", errorJSON: true},
		{name: "json-separated", args: []string{"--", "--json", "-"}, wantError: "`-` must be the only task argument"},
		{name: "help", args: []string{"--help"}, tty: true, want: startup{Help: true}},
		{name: "help-empty", args: []string{"--help", " ", "--session-id="}, tty: true, want: startup{Help: true}},
		{name: "help-unknown", args: []string{"--help", "--bogus"}, tty: true, want: startup{Help: true}},
		{name: "help-json", args: []string{"--json", "--help"}, tty: true, want: startup{Help: true}},
		{name: "json-tty", args: []string{"--json"}, tty: true, wantError: taskRequired, errorJSON: true},
		{name: "json-empty-id", args: []string{"--json", "--session-id=", "hello"}, wantError: "--session-id requires a non-empty session id", errorJSON: true},
		{name: "unknown-before-help", args: []string{"--bogus", "--help"}, tty: true, want: startup{Help: true}},
		{name: "help-missing", args: []string{"--help", "--session-id"}, tty: true, wantError: "option '--session-id <id>' argument missing"},
		{name: "missing-before-help", args: []string{"--session-id", "--help"}, tty: true, wantError: taskRequired},
		{name: "short-help-chain", args: []string{"-hh"}, tty: true, wantError: "unknown option '-hh'"},
		{name: "short-help-tail", args: []string{"-hello"}, tty: true, wantError: "unknown option '-hello'"},
		{name: "value-separator", args: []string{"--session-id", "--", "hello"}, want: startup{Task: "hello", SessionID: "--", Resume: true}},
		{name: "bom-blank", args: []string{"\ufeff"}, wantError: taskRequired},
		{name: "non-js-whitespace", args: []string{""}, want: startup{Task: ""}},
		{name: "bom-id", args: []string{"--session-id=\ufeff", "hello"}, wantError: "--session-id requires a non-empty session id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parse(tc.args, tc.tty)
			if tc.wantError != "" {
				if err == nil || err.Error() != tc.wantError {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
				if got := jsonRequested(tc.args); got != tc.errorJSON {
					t.Fatalf("error JSON = %t, want %t", got, tc.errorJSON)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.want.Help {
				if !got.Help {
					t.Fatal("help was not selected")
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("startup = %#v, want %#v", got, tc.want)
			}
		})
	}
}

type forbiddenReader struct{}

func (forbiddenReader) Read([]byte) (int, error) { panic("stdin must not be read") }
func TestInputPreservesTextAndReadsOnlyWhenSelected(t *testing.T) {
	for _, args := range [][]string{{"hello  world\n"}, {"one", "two"}} {
		s, err := parse(args, false)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Input(context.Background(), forbiddenReader{})
		if err != nil || got != strings.Join(args, " ") {
			t.Fatalf("input=%q err=%v", got, err)
		}
	}
	for _, args := range [][]string{nil, {"-"}} {
		s, err := parse(args, false)
		if err != nil {
			t.Fatal(err)
		}
		text := "你好\n\n" + strings.Repeat("x", (1<<20)+1) + "\n"
		got, err := s.Input(context.Background(), strings.NewReader(text))
		if err != nil || got != text {
			t.Fatalf("stdin not preserved: len=%d err=%v", len(got), err)
		}
		if _, err = s.Input(context.Background(), strings.NewReader(" \n\t")); err == nil {
			t.Fatal("accepted blank stdin")
		}
		if _, err = s.Input(context.Background(), strings.NewReader(string([]byte{0xff}))); err == nil {
			t.Fatal("accepted invalid UTF-8")
		}
	}
}

var _ io.Reader = forbiddenReader{}

func TestPluginConfigurationSchemaAndValidation(t *testing.T) {
	registration := Extension().Registrations[0]
	var document any
	if err := json.Unmarshal(registration.Descriptor.ConfigSchema, &document); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("headless.json", document); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("headless.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"default", `{}`, true},
		{"explicit", `{"model":"test-text"}`, true},
		{"empty", `{"model":""}`, false},
		{"blank", `{"model":" "}`, false},
		{"invalid-id", `{"model":"provider/model"}`, false},
		{"unknown", `{"model":"test-text","unknown":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var config any
			if err := json.Unmarshal([]byte(tc.raw), &config); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(config); (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
	for _, model := range []string{"", "test-text", "provider/model", " "} {
		p := Plugin{Model: model}
		valid := model == "" || model == "test-text"
		if err := p.Validate(); (err == nil) != valid {
			t.Fatal(model, err)
		}
	}
}
