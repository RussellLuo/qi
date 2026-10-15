package tools

import (
	"reflect"
	"testing"
	"time"
)

func TestSnapshotDescriptionsCopyAndGeneration(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()
	first := echoDefinition("first")
	first.Timeout, first.FailureMode, first.DeferLoading = time.Second, ToolFailureAbort, true
	revoke, err := runtime.Register(newTestOwner("first", 1), first)
	if err != nil {
		t.Fatal(err)
	}
	old, err := runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	want := DefinitionInfo{
		Name: first.Name, Description: first.Description, Parameters: first.Parameters.Clone(), Output: first.Output.Clone(),
		Timeout: first.Timeout, FailureMode: first.FailureMode, DeferLoading: first.DeferLoading,
	}
	got := old.Descriptions()
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("descriptions = %#v, want %#v", got, want)
	}
	got[0].Parameters[0], got[0].Output[0], got[0].Description = '!', '!', "mutated"
	if !reflect.DeepEqual(old.Descriptions()[0], want) {
		t.Fatal("description did not own schema copies")
	}
	revoke()
	if _, err := runtime.Register(newTestOwner("second", 2), echoDefinition("second")); err != nil {
		t.Fatal(err)
	}
	current, err := runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if current.Descriptions()[0].Description != "second" || !reflect.DeepEqual(old.Descriptions()[0], want) {
		t.Fatal("description crossed generations")
	}
	result := execute(t, old, `{"text":"hello"}`)
	if string(result.Value) != `{"value":"first:hello"}` {
		t.Fatalf("old execution = %s", result.Value)
	}
	old.Close()
	if len(old.Descriptions()) != 0 || len((*Snapshot)(nil).Descriptions()) != 0 {
		t.Fatal("closed Snapshot exposed descriptions")
	}
}

func TestSnapshotDescriptionsUseCompiledDefaults(t *testing.T) {
	runtime := NewRuntime()
	defer runtime.Close()
	d := echoDefinition("defaults")
	d.Parameters, d.Output, d.FailureMode = nil, nil, ""
	if _, err := runtime.Register(newTestOwner("defaults", 1), d); err != nil {
		t.Fatal(err)
	}
	snapshot, err := runtime.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	info := snapshot.Descriptions()[0]
	if string(info.Parameters) != `{}` || string(info.Output) != `{}` || info.FailureMode != ToolFailureResult {
		t.Fatalf("compiled defaults = %#v", info)
	}
	// Describing a Snapshot does not consume its ability to execute.
	execute(t, snapshot, `{"text":"hello"}`)
}
