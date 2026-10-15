package protocol_test

import (
	"encoding/json"
	"testing"

	"github.com/RussellLuo/qi/kernel/core/protocol"
)

func TestConfigAndHeaderEquality(t *testing.T) {
	config := protocol.CallConfig{Provider: "p", Model: "m", Stop: []string{"END"}}
	tool := protocol.ToolSchema{Name: "echo", Description: "Echo text", Parameters: json.RawMessage(`{"type":"object"}`)}
	header := protocol.EpochHeader{Config: config, Tools: []protocol.ToolSchema{tool}}
	for _, tc := range []struct {
		name                     string
		left, right              protocol.EpochHeader
		configEqual, headerEqual bool
	}{
		{"equal", header, protocol.EpochHeader{Config: config.Clone(), Tools: []protocol.ToolSchema{tool}}, true, true},
		{"stop presence", protocol.EpochHeader{Config: protocol.CallConfig{Provider: "p", Model: "m"}}, protocol.EpochHeader{Config: protocol.CallConfig{Provider: "p", Model: "m", Stop: []string{}}}, false, false},
		{"tool addition", header, protocol.EpochHeader{Config: config, Tools: []protocol.ToolSchema{tool, {Name: "extra", Parameters: json.RawMessage(`{"type":"object"}`)}}}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := protocol.CallConfigEquals(tc.left.Config, tc.right.Config); got != tc.configEqual {
				t.Fatalf("config equality = %t, want %t", got, tc.configEqual)
			}
			if got := protocol.HeaderEquals(tc.left, tc.right); got != tc.headerEqual {
				t.Fatalf("header equality = %t, want %t", got, tc.headerEqual)
			}
		})
	}
}
